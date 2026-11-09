package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func shotServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	s := New(Options{Token: testToken, Source: (&fakeSource{}).load, ShotsDir: dir})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, dir
}

// fakeApp connects like a window and answers each capture request with reply.
func fakeApp(t *testing.T, ts *httptest.Server, reply func(captureRequest) captureReply) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/app/connect?token=" + testToken + "&kind=web"
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	go func() {
		for {
			var req captureRequest
			if wsjson.Read(ctx, c, &req) != nil {
				return
			}
			rep := reply(req)
			rep.Type, rep.ID = "capture", req.ID
			if wsjson.Write(ctx, c, rep) != nil {
				return
			}
		}
	}()
}

func screenshot(t *testing.T, ts *httptest.Server) (int, map[string]any) {
	t.Helper()
	// the client registers asynchronously: retry briefly while "no app window"
	var code int
	var body map[string]any
	for i := 0; i < 50; i++ {
		resp := get(t, ts.URL+"/api/app/screenshot", map[string]string{"Authorization": "Bearer " + testToken})
		code, body = resp.StatusCode, decode(t, resp)
		if code != http.StatusServiceUnavailable {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return code, body
}

func TestAppScreenshot_WritesPNGAndText(t *testing.T) {
	ts, dir := shotServer(t)
	fakeApp(t, ts, func(captureRequest) captureReply {
		return captureReply{PNG: base64.StdEncoding.EncodeToString([]byte("PNGDATA")), Text: "Needs you\n1 ask", Source: "dom"}
	})
	code, body := screenshot(t, ts)
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("code %d body %v", code, body)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "app.png")); string(got) != "PNGDATA" {
		t.Errorf("app.png = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "app-text.txt")); string(got) != "Needs you\n1 ask" {
		t.Errorf("app-text.txt = %q", got)
	}
	if body["png"] != filepath.Join(dir, "app.png") || body["source"] != "dom" || body["text"] != "Needs you\n1 ask" {
		t.Errorf("body %v", body)
	}
}

func TestAppScreenshot_TextOnlyWarns(t *testing.T) {
	ts, dir := shotServer(t)
	fakeApp(t, ts, func(captureRequest) captureReply { return captureReply{Text: "hello", Error: "png failed"} })
	code, body := screenshot(t, ts)
	if code != http.StatusOK || body["warning"] != "png failed" || body["png"] != nil {
		t.Fatalf("code %d body %v", code, body)
	}
	if _, err := os.Stat(filepath.Join(dir, "app.png")); err == nil {
		t.Error("app.png written without a png")
	}
}

func TestAppScreenshot_NothingFromApp(t *testing.T) {
	ts, _ := shotServer(t)
	fakeApp(t, ts, func(captureRequest) captureReply { return captureReply{Error: "boom"} })
	code, body := screenshot(t, ts)
	if code != http.StatusBadGateway || !strings.Contains(body["error"].(string), "boom") {
		t.Fatalf("code %d body %v", code, body)
	}
}

func TestAppScreenshot_NoApp(t *testing.T) {
	ts, _ := shotServer(t)
	resp := get(t, ts.URL+"/api/app/screenshot", map[string]string{"Authorization": "Bearer " + testToken})
	body := decode(t, resp)
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body["error"].(string), "no app window connected") {
		t.Fatalf("code %d body %v", resp.StatusCode, body)
	}
}

func TestAppScreenshot_NeedsToken(t *testing.T) {
	ts, _ := shotServer(t)
	for _, p := range []string{"/api/app/screenshot", "/api/app/connect"} {
		if resp := get(t, ts.URL+p, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without token: %d", p, resp.StatusCode)
		}
	}
}

func TestAppScreenshot_Timeout(t *testing.T) {
	old := captureTimeout
	captureTimeout = 100 * time.Millisecond
	t.Cleanup(func() { captureTimeout = old })
	ts, _ := shotServer(t)
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	fakeApp(t, ts, func(captureRequest) captureReply { <-block; return captureReply{} })
	code, body := screenshot(t, ts)
	if code != http.StatusGatewayTimeout {
		t.Fatalf("code %d body %v", code, body)
	}
}

func TestAllowed_AppShot(t *testing.T) {
	if !Allowed([]string{"app", "shot"}) || Allowed([]string{"app"}) || Allowed([]string{"app", "install"}) {
		t.Error("only `app shot` may run through /api/cmd")
	}
}

func decode(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return m
}
