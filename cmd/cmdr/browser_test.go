package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeBrowserEnv(t *testing.T) (home string, launched *[]string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	origEnv, origLaunch, origWait := browserGetenv, browserLaunch, browserStartWait
	t.Cleanup(func() { browserGetenv, browserLaunch, browserStartWait = origEnv, origLaunch, origWait })
	browserStartWait = 0
	browserGetenv = func(k string) string {
		if k == "CMDR_CHROME" {
			return "/fake/chrome"
		}
		return ""
	}
	var got []string
	browserLaunch = func(chrome string, args []string) (func() error, error) {
		got = append([]string{chrome}, args...)
		// The start page must exist while Chrome runs.
		if last := args[len(args)-1]; strings.HasPrefix(last, "file://") {
			b, err := os.ReadFile(strings.TrimPrefix(last, "file://"))
			if err != nil || !strings.Contains(string(b), "https://linear.app/login") {
				t.Errorf("start page: %v %q", err, b)
			}
		}
		return func() error { return nil }, nil
	}
	return home, &got
}

func TestBrowserLogin_OpensHeadedChromeOnProfile(t *testing.T) {
	home, launched := fakeBrowserEnv(t)
	var out, errb bytes.Buffer
	if code := runBrowser([]string{"login"}, &out, &errb); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errb.String())
	}
	profile := filepath.Join(home, "browser-profile")
	fi, err := os.Stat(profile)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("profile: %v %v", fi, err)
	}
	args := strings.Join(*launched, " ")
	for _, want := range []string{"/fake/chrome", "--user-data-dir=" + profile, "--no-first-run", "--no-default-browser-check", "file://"} {
		if !strings.Contains(args, want) {
			t.Errorf("launch args missing %q: %s", want, args)
		}
	}
	if strings.Contains(args, "headless") {
		t.Error("login must be headed")
	}
	for _, want := range []string{"browser profile: " + profile, "Notion   signed in: no", "Slack    signed in: no"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("summary missing %q:\n%s", want, out.String())
		}
	}
}

func TestBrowserLogin_URLFlag(t *testing.T) {
	_, launched := fakeBrowserEnv(t)
	var out, errb bytes.Buffer
	if code := runBrowser([]string{"login", "--url", "https://x.example/"}, &out, &errb); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errb.String())
	}
	if l := *launched; l[len(l)-1] != "https://x.example/" {
		t.Errorf("start url = %v", l)
	}
}

func TestBrowserLogin_ProfileAlreadyOpen(t *testing.T) {
	home, launched := fakeBrowserEnv(t)
	profile := filepath.Join(home, "browser-profile")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	host, _ := os.Hostname()
	if err := os.Symlink(fmt.Sprintf("%s-%d", host, os.Getpid()), filepath.Join(profile, "SingletonLock")); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := runBrowser([]string{"login"}, &out, &errb); code != 1 {
		t.Fatalf("code=%d", code)
	}
	if *launched != nil || !strings.Contains(errb.String(), "already has the inspector profile open") {
		t.Errorf("launched=%v stderr=%s", *launched, errb.String())
	}
}

func TestBrowserStatus_NoProfile(t *testing.T) {
	fakeBrowserEnv(t)
	var out, errb bytes.Buffer
	if code := runBrowser([]string{"status"}, &out, &errb); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(out.String(), "run `cmdr browser login`") {
		t.Errorf("status = %s", out.String())
	}
}
