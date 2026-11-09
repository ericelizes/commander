package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runShotCommand runs `cmdr app shot <args>` against a fake cmdrd and returns
// what it printed on stdout.
func runShotCommand(t *testing.T, args ...string) string {
	t.Helper()
	home := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/app/screenshot" || r.Header.Get("Authorization") != "Bearer tok en" {
			http.Error(w, "bad", 400)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"png":"/h/shots/app.png","text_path":"/h/shots/app-text.txt","text":"private queue line","source":"electron-capturePage"}`))
	}))
	defer srv.Close()
	writeAPIFiles(t, home, srv.URL)
	t.Setenv("COMMANDER_HOME", home)
	stdout, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	if code := runAppShot(args, stdout, os.Stderr); code != 0 {
		t.Fatalf("app shot %v exit = %d", args, code)
	}
	got, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

// The capture holds the user's private queue and chat text: anything an agent
// prints lands in its transcript, so stdout gets it only when asked by name.
func TestAppShotPrintsOnlyPathsUnlessTextStdout(t *testing.T) {
	for _, args := range [][]string{nil, {"--text"}} {
		got := runShotCommand(t, args...)
		if want := "png: /h/shots/app.png\ntext: /h/shots/app-text.txt\nsource: electron-capturePage\n"; got != want {
			t.Errorf("app shot %v printed %q, want %q", args, got, want)
		}
	}
	got := runShotCommand(t, "--text-stdout")
	for _, want := range []string{"png: /h/shots/app.png", "text: /h/shots/app-text.txt", "source: electron-capturePage", "private queue line"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestAppShotReportsNoApp(t *testing.T) {
	home := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"ok":false,"error":"no app window connected: open the Commander app"}`))
	}))
	defer srv.Close()
	writeAPIFiles(t, home, srv.URL)
	if _, err := appShot(home, false); err == nil || !strings.Contains(err.Error(), "no app window connected") {
		t.Fatalf("err = %v", err)
	}
}
