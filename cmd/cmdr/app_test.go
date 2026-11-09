package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeAPIFiles(t *testing.T, home, base string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "api-url"), []byte(base+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "api-token"), []byte("tok en\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAppWebLinkPutsTheTokenInTheFragment(t *testing.T) {
	home := t.TempDir()
	writeAPIFiles(t, home, "http://127.0.0.1:8787")
	link, err := appWebLink(home, "http://127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	if link != "http://127.0.0.1:8787/app/#token=tok+en" {
		t.Fatalf("link = %q", link)
	}
	if portOf("http://127.0.0.1:8791") != "8791" {
		t.Fatal("portOf")
	}
}

func TestHasDisplay(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cases := []struct {
		goos string
		env  map[string]string
		want bool
	}{
		{"darwin", nil, true},
		{"linux", nil, false},
		{"linux", map[string]string{"DISPLAY": ":0"}, true},
		{"linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, true},
	}
	for _, c := range cases {
		if got := hasDisplay(c.goos, env(c.env)); got != c.want {
			t.Errorf("hasDisplay(%s, %v) = %v", c.goos, c.env, got)
		}
	}
}

func TestEnsureAPIStartsTheDaemonOnlyWhenDown(t *testing.T) {
	home := t.TempDir()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()

	started := 0
	startDaemonFn = func() error { started++; return nil }
	defer func() { startDaemonFn = startDaemon }()

	writeAPIFiles(t, home, up.URL)
	if base, err := ensureAPI(home); err != nil || base != up.URL || started != 0 {
		t.Fatalf("up: base=%q err=%v started=%d", base, err, started)
	}

	// Down: cmdrd gets started; when it never answers, the error says so.
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	writeAPIFiles(t, home, down.URL)
	appWait = 300 * time.Millisecond
	defer func() { appWait = 10 * time.Second }()
	if _, err := ensureAPI(home); err == nil || !strings.Contains(err.Error(), "did not answer") || started != 1 {
		t.Fatalf("down: err=%v started=%d", err, started)
	}

	startDaemonFn = func() error { return errors.New("no cmdrd") }
	if _, err := ensureAPI(home); err == nil || !strings.Contains(err.Error(), "could not be started") {
		t.Fatalf("start failure: err=%v", err)
	}
}

func TestRunAppWebPrintsTheLink(t *testing.T) {
	home := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	writeAPIFiles(t, home, srv.URL)
	t.Setenv("COMMANDER_HOME", home)
	launchAppFn = func(string) (string, error) { t.Fatal("--web must not launch the desktop app"); return "", nil }
	defer func() { launchAppFn = launchApp }()

	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	errf, _ := os.CreateTemp(t.TempDir(), "err")
	if code := runApp([]string{"--web"}, out, errf); code != 0 {
		t.Fatalf("exit %d", code)
	}
	raw, _ := os.ReadFile(out.Name())
	if strings.TrimSpace(string(raw)) != srv.URL+"/app/#token=tok+en" {
		t.Fatalf("stdout = %q", raw)
	}
}

func TestEnsureAPIWaitsOnALiveLockHolder(t *testing.T) {
	home := t.TempDir()
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	writeAPIFiles(t, home, down.URL)
	started := 0
	startDaemonFn = func() error { started++; return nil }
	oldHolder := lockHolderFn
	lockHolderFn = func(string) int { return 4242 }
	appWait = 300 * time.Millisecond
	defer func() { startDaemonFn, lockHolderFn, appWait = startDaemon, oldHolder, 10*time.Second }()

	_, err := ensureAPI(home)
	if started != 0 {
		t.Fatalf("started a second cmdrd while pid 4242 holds the lock")
	}
	if err == nil || !strings.Contains(err.Error(), "cmdrd (pid 4242) is running") {
		t.Fatalf("err = %v", err)
	}
}
