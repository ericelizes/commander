package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeVite stands in for `npm run dev:renderer`: the page under /app/, a
// module, and the HMR WebSocket (Vite's base path, subprotocol vite-hmr).
func fakeVite(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/app/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "websocket" {
			c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"vite-hmr"}, InsecureSkipVerify: true})
			if err != nil {
				return
			}
			defer c.CloseNow()
			_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"type":"connected"}`))
			return
		}
		switch r.URL.Path {
		case "/app/":
			io.WriteString(w, `<script type="module" src="/app/@vite/client"></script>dev page host=`+r.Host)
		case "/app/src/main.tsx":
			io.WriteString(w, "export const live = true")
		default:
			http.NotFound(w, r)
		}
	})
	return httptest.NewServer(mux)
}

func builtApp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("bundled page"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func getBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestApp_ProxiesTheLiveDevServerIncludingHMR(t *testing.T) {
	vite := fakeVite(t)
	defer vite.Close()
	ts := httptest.NewServer(New(Options{Token: testToken, Source: (&fakeSource{}).load, AppDir: builtApp(t), AppDevURL: vite.URL}).Handler())
	defer ts.Close()

	page := getBody(t, ts.URL+"/app/")
	if !strings.Contains(page, "dev page") || !strings.Contains(page, "host="+strings.TrimPrefix(vite.URL, "http://")) {
		t.Fatalf("/app/ = %q, want the dev server's page (Host rewritten to it)", page)
	}
	if got := getBody(t, ts.URL+"/app/src/main.tsx"); got != "export const live = true" {
		t.Fatalf("module = %q", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/app/?token=hmr", &websocket.DialOptions{Subprotocols: []string{"vite-hmr"}})
	if err != nil {
		t.Fatalf("HMR websocket through cmdrd: %v", err)
	}
	defer c.CloseNow()
	_, msg, err := c.Read(ctx)
	if err != nil || string(msg) != `{"type":"connected"}` {
		t.Fatalf("hmr frame = %q, %v", msg, err)
	}
}

func TestApp_ServesTheBundleWhenTheDevServerIsDown(t *testing.T) {
	vite := fakeVite(t)
	url := vite.URL
	vite.Close() // nothing listens there now
	ts := httptest.NewServer(New(Options{Token: testToken, Source: (&fakeSource{}).load, AppDir: builtApp(t), AppDevURL: url}).Handler())
	defer ts.Close()
	if got := getBody(t, ts.URL+"/app/"); got != "bundled page" {
		t.Fatalf("/app/ = %q, want the bundle", got)
	}
}

func TestApp_IgnoresANonViteServerOnTheDevPort(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "some other app") }))
	defer other.Close()
	ts := httptest.NewServer(New(Options{Token: testToken, Source: (&fakeSource{}).load, AppDir: builtApp(t), AppDevURL: other.URL}).Handler())
	defer ts.Close()
	if got := getBody(t, ts.URL+"/app/"); got != "bundled page" {
		t.Fatalf("/app/ = %q, want the bundle", got)
	}
}

func TestAppDev_ProbeIsCachedThenRechecked(t *testing.T) {
	vite := fakeVite(t)
	defer vite.Close()
	h := appHandler(builtApp(t), vite.URL).(*appDev)
	now := time.Unix(1000, 0)
	h.now = func() time.Time { return now }
	if !h.alive() {
		t.Fatal("dev server up: alive() = false")
	}
	vite.Close()
	if !h.alive() {
		t.Fatal("within the TTL the cached answer holds")
	}
	now = now.Add(devProbeTTL + time.Millisecond)
	if h.alive() {
		t.Fatal("after the TTL a dead dev server is noticed")
	}
}
