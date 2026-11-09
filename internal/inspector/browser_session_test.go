package inspector

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// signedInFetcher is browserFetcher with a persistent profile signed in to
// Notion (and nothing else).
func signedInFetcher(t *testing.T, ex Exec) (Fetcher, string) {
	now := time.Now()
	resetSessionCache(t, &now)
	f, dir := browserFetcher(t, ex, true)
	f.Browser.Profile = filepath.Join(dir, "browser-profile")
	writeCookieDB(t, f.Browser.Profile, []fixtureCookie{{".www.notion.so", "token_v2", now.Add(24 * time.Hour)}})
	return f, dir
}

func userDataDir(args []string) string {
	for _, a := range args {
		if p, ok := strings.CutPrefix(a, "--user-data-dir="); ok {
			return p
		}
	}
	return ""
}

func TestBrowser_UsesSignedInProfile(t *testing.T) {
	var args []string
	f, _ := signedInFetcher(t, fakeChrome(&args, true, nil))
	card, err := f.Fetch(context.Background(), Target{Kind: KindURL, Target: "https://www.notion.so/Spec-123"})
	if err != nil {
		t.Fatal(err)
	}
	if card.Renderer != RendererBrowser || card.Image == "" || card.Meta[0] != BrowserSignedInHint {
		t.Fatalf("card = %+v", card)
	}
	if got := userDataDir(args); got != f.Browser.Profile {
		t.Errorf("--user-data-dir = %q, want the persistent profile", got)
	}
	if _, err := os.Stat(f.Browser.Profile); err != nil {
		t.Error("persistent profile must be kept")
	}

	// Same profile, a site with no session: honest hint.
	card, _ = f.Fetch(context.Background(), Target{Kind: KindURL, Target: "https://example.com/"})
	if userDataDir(args) != f.Browser.Profile || card.Meta[0] != BrowserLoginHint {
		t.Errorf("example.com: profile=%q meta=%v", userDataDir(args), card.Meta)
	}
}

func TestBrowser_LoginWindowOpen(t *testing.T) {
	var args []string
	f, dir := signedInFetcher(t, fakeChrome(&args, true, nil))
	host, _ := os.Hostname()
	if err := os.Symlink(fmt.Sprintf("%s-%d", host, os.Getpid()), filepath.Join(f.Browser.Profile, "SingletonLock")); err != nil {
		t.Fatal(err)
	}
	target := Target{Kind: KindURL, Target: "https://example.com/a", Renderer: RendererBrowser}

	// No cached shot: a throwaway (logged-out) shot that says why.
	card, err := f.Fetch(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if p := userDataDir(args); p == f.Browser.Profile || !strings.HasPrefix(p, dir) {
		t.Errorf("must not touch the open profile, got %q", p)
	}
	if card.Image == "" || card.Meta[0] != BrowserLoginHint+" · login window open" {
		t.Errorf("card = %+v", card)
	}

	// A cached shot exists now: show it, no Chrome launch.
	args = nil
	card, _ = f.Fetch(context.Background(), target)
	if args != nil || card.Image != ShotPath(dir, target.Target) || card.Meta[0] != "login window open · showing last shot" {
		t.Errorf("cached card = %+v args=%v", card, args)
	}
}

func TestBrowser_ProfileBusyFallsBack(t *testing.T) {
	var args []string
	f, _ := signedInFetcher(t, fakeChrome(&args, true, nil))
	unlock, err := LockProfile(context.Background(), f.Browser.Profile)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	card, _ := f.browser(ctx, f.Exec, Target{Kind: KindURL, Target: "https://example.com/b"})
	if userDataDir(args) == f.Browser.Profile || len(card.Meta) == 0 || !strings.Contains(card.Meta[0], "profile busy") {
		t.Errorf("busy: profile=%q card=%+v", userDataDir(args), card)
	}
}

func TestRenderer_SignedInNotionLinearBecomePictures(t *testing.T) {
	var args []string
	f, _ := signedInFetcher(t, fakeChrome(&args, true, nil))
	notion := Target{Kind: KindURL, Target: "https://www.notion.so/Spec-123"}
	linear := Target{Kind: KindURL, Target: "https://linear.app/f/issue/TEAM-1/x"} // no linear cookie
	pr := Target{Kind: KindURL, Target: "https://github.com/o/r/pull/1"}
	if got := f.Renderer(notion); got != RendererBrowser {
		t.Errorf("signed-in notion = %s", got)
	}
	if got := f.Renderer(linear); got != RendererLinear {
		t.Errorf("linear without session = %s", got)
	}
	if got := f.Renderer(pr); got != RendererGitHubPR {
		t.Errorf("PR = %s", got)
	}

	// Without graphics the text card stays.
	off := f
	off.Browser.Enabled = false
	if got := off.Renderer(notion); got != RendererNotion {
		t.Errorf("no graphics = %s", got)
	}

	// An explicit renderer wins over the session: no Chrome launch.
	var ran []string
	f.Exec = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		ran = append(ran, name)
		return nil, errors.New("no claude")
	}
	notion.Renderer = RendererNotion
	card, _ := f.Fetch(context.Background(), notion)
	if card.Renderer != RendererNotion || card.Image != "" {
		t.Errorf("explicit notion = %+v", card)
	}
	for _, name := range ran {
		if name == "/fake/chrome" {
			t.Error("explicit notion renderer must not launch Chrome")
		}
	}
}
