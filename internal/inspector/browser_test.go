package inspector

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeChrome writes a PNG to the --screenshot= path and records its args.
func fakeChrome(gotArgs *[]string, write bool, err error) Exec {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		*gotArgs = append([]string{name}, args...)
		if write {
			for _, a := range args {
				if p, ok := strings.CutPrefix(a, "--screenshot="); ok {
					if werr := os.WriteFile(p, []byte("\x89PNG fake"), 0o644); werr != nil {
						return nil, werr
					}
				}
			}
		}
		return []byte("written to file"), err
	}
}

func browserFetcher(t *testing.T, ex Exec, enabled bool) (Fetcher, string) {
	dir := t.TempDir()
	return Fetcher{Exec: ex, Getenv: func(string) string { return "" }, Browser: BrowserConfig{
		Enabled: enabled, Dir: dir, Chrome: "/fake/chrome", CellW: 18, CellH: 42,
	}}, dir
}

func TestBrowser_ScreenshotsWithThrowawayProfile(t *testing.T) {
	var args []string
	f, dir := browserFetcher(t, fakeChrome(&args, true, nil), true)
	card, err := f.Fetch(context.Background(), Target{Kind: KindURL, Target: "https://example.com/a", Cols: 40, Rows: 50})
	if err != nil {
		t.Fatal(err)
	}
	if card.Renderer != RendererWeb || card.Image != ShotPath(dir, "https://example.com/a") || card.ImageCols != 40 || card.ImageRows != 50 {
		t.Fatalf("card = %+v", card)
	}
	if _, err := os.Stat(card.Image); err != nil {
		t.Errorf("png not cached: %v", err)
	}
	joined := strings.Join(args, " ")
	// 40x50 cells of 18x42 px = 720x2100 px; HiDPI would be 360 CSS px, below
	// Chrome's 500 minimum, so 500 CSS px at 1.44x.
	for _, want := range []string{"/fake/chrome", "--headless=new", "--hide-scrollbars", "--force-device-scale-factor=1.44", "--window-size=500,1458", "https://example.com/a"} {
		if !strings.Contains(joined, want) {
			t.Errorf("chrome args missing %q: %s", want, joined)
		}
	}
	var profile string
	for _, a := range args {
		if p, ok := strings.CutPrefix(a, "--user-data-dir="); ok {
			profile = p
		}
	}
	if !strings.HasPrefix(profile, dir+string(filepath.Separator)) {
		t.Errorf("profile %q must live under %s", profile, dir)
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Error("throwaway profile must be removed")
	}
	if len(card.Meta) == 0 || card.Meta[0] != BrowserLoginHint {
		t.Errorf("meta = %v", card.Meta)
	}
}

func TestShotGeometry(t *testing.T) {
	cases := []struct {
		pxW, pxH, cellW int
		cssW, cssH      int
		scale           float64
	}{
		{1800, 1000, 18, 900, 500, 2},    // Retina, wide pane: 2x
		{900, 1000, 9, 900, 1000, 1},     // 1x, wide pane
		{324, 1400, 9, 500, 2160, 0.648}, // narrow: laid out at 500 and scaled down
	}
	for _, tc := range cases {
		w, h, s := shotGeometry(tc.pxW, tc.pxH, tc.cellW)
		if w != tc.cssW || h != tc.cssH || s != tc.scale {
			t.Errorf("shotGeometry(%d,%d,%d) = %d,%d,%v want %d,%d,%v", tc.pxW, tc.pxH, tc.cellW, w, h, s, tc.cssW, tc.cssH, tc.scale)
		}
	}
}

func TestBrowser_ExplicitRendererOverridesDetect(t *testing.T) {
	var args []string
	f, _ := browserFetcher(t, fakeChrome(&args, true, nil), true)
	card, _ := f.Fetch(context.Background(), Target{Kind: KindURL, Target: "http://localhost:5173", Renderer: RendererBrowser})
	if card.Renderer != RendererBrowser || card.Image == "" {
		t.Fatalf("card = %+v", card)
	}
}

func TestBrowser_FallsBackToLinkCardWithHint(t *testing.T) {
	var args []string
	// No kitty graphics: no Chrome launch, link card + hint.
	f, _ := browserFetcher(t, fakeChrome(&args, true, nil), false)
	card, err := f.Fetch(context.Background(), Target{Kind: KindURL, Target: "https://example.com/a"})
	if err != nil || card.Image != "" || args != nil || !strings.Contains(strings.Join(card.Meta, ""), "Ghostty") {
		t.Fatalf("no-graphics card = %+v err=%v args=%v", card, err, args)
	}
	if !strings.Contains(card.Markdown, "Press o") {
		t.Errorf("link card body missing: %q", card.Markdown)
	}

	// Chrome fails: the error is surfaced, the card is the link card.
	f, _ = browserFetcher(t, fakeChrome(&args, false, errors.New("exit status 1")), true)
	card, err = f.Fetch(context.Background(), Target{Kind: KindURL, Target: "https://example.com/a"})
	if err == nil || card.Image != "" || !strings.Contains(card.Err, "chrome screenshot") {
		t.Fatalf("failed card = %+v err=%v", card, err)
	}

	// Chrome exits 0 but writes nothing.
	f, _ = browserFetcher(t, fakeChrome(&args, false, nil), true)
	if card, err = f.Fetch(context.Background(), Target{Kind: KindURL, Target: "https://example.com/a"}); err == nil || card.Image != "" {
		t.Fatalf("no-file card = %+v err=%v", card, err)
	}
}

func TestBrowser_NoChromeHint(t *testing.T) {
	home := t.TempDir() // no browsers/ under it: neither headless-shell nor Chrome
	f := Fetcher{Getenv: func(k string) string {
		if k == "COMMANDER_HOME" {
			return home
		}
		return ""
	}, Browser: BrowserConfig{Enabled: true, Dir: t.TempDir()}}
	orig := chromeCandidates
	chromeCandidates = []string{"/nonexistent/chrome"}
	t.Cleanup(func() { chromeCandidates = orig })
	card, err := f.Fetch(context.Background(), Target{Kind: KindURL, Target: "https://example.com/a"})
	if err != nil || card.Image != "" || !strings.Contains(strings.Join(card.Meta, ""), "Google Chrome") {
		t.Fatalf("card = %+v err=%v", card, err)
	}
}

func TestBrowser_AuthenticatedHostsKeepTheirRenderers(t *testing.T) {
	for target, want := range map[string]string{
		"https://www.notion.so/x-123":          RendererNotion,
		"https://linear.app/f/issue/TEAM-1/x":  RendererLinear,
		"https://app.datadoghq.com/dashboard/": RendererLink,
		"https://example.com/":                 RendererWeb,
	} {
		if got := Detect(KindURL, target); got != want {
			t.Errorf("Detect(%s) = %s, want %s", target, got, want)
		}
	}
	if DefaultRefreshSecs(KindURL, "https://example.com/", "") != 300 || DefaultRefreshSecs(KindURL, "http://localhost:3000", RendererBrowser) != 300 {
		t.Error("screenshot tabs refresh every 300s by default")
	}
}

func TestFindHeadlessShell_PrefersTheInstalledShellAndPicksTheRightFlag(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "browsers", "chrome-headless-shell", "mac_arm-155", "chrome-headless-shell-mac-arm64", "chrome-headless-shell")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		if k == "COMMANDER_HOME" {
			return home
		}
		return ""
	}
	if got := FindHeadlessShell(getenv); got != bin {
		t.Fatalf("FindHeadlessShell = %q, want %q", got, bin)
	}
	if headlessFlag(bin) != "--headless" || headlessFlag("/Applications/Google Chrome.app/x/Google Chrome") != "--headless=new" {
		t.Fatal("headless flag per binary")
	}
}
