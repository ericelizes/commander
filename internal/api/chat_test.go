package api

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/example-user/commander/internal/testguard"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/chat/chattest"
)

func TestMain(m *testing.M) {
	chat.MaybeRunHost()
	chattest.MaybeRunFake()
	cleanup := testguard.Isolate()
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func newChatServer(t *testing.T) (*chat.Engine, *httptest.Server) {
	t.Helper()
	t.Setenv(chattest.EnvVar, "1")
	t.Setenv(chattest.DirVar, t.TempDir())
	projects := t.TempDir()
	t.Setenv(chattest.ProjectsVar, projects)
	work := t.TempDir()
	id := agentrt.NewClaudeSessionID()
	e := chat.New(chat.Options{
		Home: t.TempDir(), Binary: os.Args[0], ProjectsDir: projects,
		Procs: func(context.Context, string) ([]chat.Proc, error) { return nil, nil },
		Resolver: chat.ResolverFunc(func(_ context.Context, role string) (chat.Spec, error) {
			return chat.Spec{Role: role, Agent: role, SessionID: id, Cwd: work}, nil
		}),
	})
	t.Cleanup(func() { _ = e.Stop(context.Background(), "operator-api") })
	src := &fakeSource{}
	src.set("x")
	s := New(Options{Token: testToken, Source: src.load, Chat: e})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return e, ts
}

func postChat(t *testing.T, ts *httptest.Server, path string, body any) (int, ChatResponse) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", ts.URL+path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out ChatResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func readUntil(t *testing.T, ctx context.Context, c *websocket.Conn, kind chat.Kind) []chat.Event {
	t.Helper()
	var seen []chat.Event
	for {
		var ev chat.Event
		if err := wsjson.Read(ctx, c, &ev); err != nil {
			t.Fatalf("read: %v (saw %d)", err, len(seen))
		}
		seen = append(seen, ev)
		if ev.Kind == kind {
			return seen
		}
	}
}

func TestChatAPIRoundTrip(t *testing.T) {
	_, ts := newChatServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Auth applies.
	if code, _ := postChatNoAuth(t, ts, "/api/chat/operator-api/start"); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	if code, r := postChat(t, ts, "/api/chat/..%2Fetc/start", nil); code != http.StatusBadRequest || r.OK {
		t.Fatalf("bad role: %d %+v", code, r)
	}
	if code, r := postChat(t, ts, "/api/chat/operator-api/send", map[string]any{"text": "hi"}); code != http.StatusConflict {
		t.Fatalf("send before start: %d %+v", code, r)
	}

	ws := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/chat/operator-api/events?since=0&token=" + testToken
	c, _, err := websocket.Dial(ctx, ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if seen := readUntil(t, ctx, c, chat.KindReplayDone); len(seen) != 1 {
		t.Fatalf("empty history replay = %+v", seen)
	}

	code, r := postChat(t, ts, "/api/chat/operator-api/send", map[string]any{"text": "run a tool", "start": true})
	if code != http.StatusOK || !r.OK || !r.Status.Running {
		t.Fatalf("send+start: %d %+v", code, r)
	}
	seen := readUntil(t, ctx, c, chat.KindPermission)
	req := seen[len(seen)-1].Request
	if code, r := postChat(t, ts, "/api/chat/operator-api/approve", map[string]any{"id": "nope", "decision": "allow"}); code != http.StatusNotFound {
		t.Fatalf("approve unknown: %d %+v", code, r)
	}
	if code, r := postChat(t, ts, "/api/chat/operator-api/approve", map[string]any{"id": req.ID, "decision": "allow"}); code != http.StatusOK || !r.OK {
		t.Fatalf("approve: %d %+v", code, r)
	}
	seen = readUntil(t, ctx, c, chat.KindResult)
	var gotResult bool
	for _, ev := range seen {
		if ev.Kind == chat.KindToolResult && ev.Tool.Content == "hi" {
			gotResult = true
		}
	}
	if !gotResult {
		t.Fatalf("no tool result over the socket: %+v", seen)
	}
	last := seen[len(seen)-1].Seq

	// A file rides along as base64 in JSON; the user event names where it was saved.
	code, r = postChat(t, ts, "/api/chat/operator-api/send", map[string]any{"text": "see this",
		"attachments": []map[string]any{{"name": "a.txt", "mime": "text/plain", "data": "aGVsbG8="}}})
	if code != http.StatusOK || !r.OK {
		t.Fatalf("send with attachment: %d %+v", code, r)
	}
	seen = readUntil(t, ctx, c, chat.KindResult)
	var saved string
	for _, ev := range seen {
		if ev.Kind == chat.KindUser && len(ev.Attachments) == 1 {
			saved = ev.Attachments[0].Path
		}
	}
	if b, err := os.ReadFile(saved); err != nil || string(b) != "hello" {
		t.Fatalf("saved attachment %q = %q (%v)", saved, b, err)
	}
	last = seen[len(seen)-1].Seq

	if code, _ := postChat(t, ts, "/api/chat/operator-api/handover", map[string]any{"confirm": true}); code != http.StatusForbidden {
		t.Fatalf("handover must be off by default: %d", code)
	}

	// Reconnect from the last seq: only newer events, then the marker.
	c2, _, err := websocket.Dial(ctx, strings.Replace(ws, "since=0", "since="+strconv.FormatUint(last, 10), 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.CloseNow()
	for _, ev := range readUntil(t, ctx, c2, chat.KindReplayDone) {
		if ev.Seq <= last && ev.Kind != chat.KindReplayDone {
			t.Fatalf("replayed old seq %d <= %d", ev.Seq, last)
		}
	}

	// List and status.
	req2, _ := http.NewRequest("GET", ts.URL+"/api/chat", nil)
	req2.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Sessions []chat.Status `json:"sessions"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list.Sessions) != 1 || list.Sessions[0].Role != "operator-api" || !list.Sessions[0].Running {
		t.Fatalf("list = %+v", list)
	}

	if code, r := postChat(t, ts, "/api/chat/operator-api/stop", nil); code != http.StatusOK || r.Status.Running {
		t.Fatalf("stop: %d %+v", code, r)
	}
	readUntil(t, ctx, c, chat.KindExit)
}

func TestChatAPIWithoutEngine(t *testing.T) {
	_, _, ts := newTestServer(t, nil)
	if code, r := postChat(t, ts, "/api/chat/commander/start", nil); code != http.StatusServiceUnavailable || r.OK {
		t.Fatalf("no engine: %d %+v", code, r)
	}
}

func postChatNoAuth(t *testing.T, ts *httptest.Server, path string) (int, error) {
	resp, err := http.Post(ts.URL+path, "application/json", strings.NewReader("{}"))
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

func TestChatSend_NotNativeYetIsDeliveredAsMailNotAnError(t *testing.T) {
	e, _ := newChatServer(t)
	var gotRole, gotText string
	s := New(Options{Token: testToken, Source: (&fakeSource{}).load, Chat: e,
		MailToRole: func(_ context.Context, role, text string) error { gotRole, gotText = role, text; return nil }})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	code, r := postChat(t, ts, "/api/chat/commander/send", map[string]any{"text": "make the sidebar wider"})
	if code != http.StatusOK || !r.OK || r.Delivered != "mail" || gotRole != "commander" || gotText != "make the sidebar wider" {
		t.Fatalf("code=%d resp=%+v role=%q text=%q", code, r, gotRole, gotText)
	}
}

func TestChatMail_ListsWaitingMessages(t *testing.T) {
	sent := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	s := New(Options{Token: testToken, Source: (&fakeSource{}).load,
		WaitingMail: func(_ context.Context, role string) ([]agentmail.Mail, error) {
			if role != "commander" {
				t.Fatalf("role = %q", role)
			}
			return []agentmail.Mail{{ID: "7", From: "manager-x", Subject: "PR ready", Body: "\n  first line\nsecond", SentAt: sent}}, nil
		}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/api/chat/commander/mail", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Mail []WaitingMail `json:"mail"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || resp.StatusCode != 200 {
		t.Fatalf("code=%d err=%v", resp.StatusCode, err)
	}
	if len(body.Mail) != 1 || body.Mail[0].From != "manager-x" || body.Mail[0].Preview != "first line" || !body.Mail[0].SentAt.Equal(sent) {
		t.Fatalf("mail = %+v", body.Mail)
	}
}

func TestChatSend_RunningChatQueuesAndNeverFallsToMail(t *testing.T) {
	e, _ := newChatServer(t)
	s := New(Options{Token: testToken, Source: (&fakeSource{}).load, Chat: e,
		MailToRole: func(_ context.Context, role, text string) error {
			t.Errorf("running chat got mail for %s: %q", role, text)
			return nil
		}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	if code, r := postChat(t, ts, "/api/chat/operator-api/start", nil); code != http.StatusOK || !r.Status.Running {
		t.Fatalf("start: %d %+v", code, r)
	}
	for _, text := range []string{"first", "second, while the first turn runs"} {
		code, r := postChat(t, ts, "/api/chat/operator-api/send", map[string]any{"text": text})
		if code != http.StatusOK || !r.OK || r.Delivered != "" {
			t.Fatalf("send %q: %d %+v", text, code, r)
		}
	}
	postChat(t, ts, "/api/chat/operator-api/stop", nil)
}

func TestChatAPIRuntime(t *testing.T) {
	_, ts := newChatServer(t)
	code, r := postChat(t, ts, "/api/chat/operator-api/runtime", map[string]any{"runtime": "codex"})
	if code != http.StatusOK || !r.OK || r.Status.Runtime != chat.RuntimeCodex || r.Status.Running {
		t.Fatalf("switch a stopped chat: %d %+v", code, r)
	}
	if code, r := postChat(t, ts, "/api/chat/operator-api/runtime", map[string]any{"runtime": "gemini"}); code != http.StatusBadRequest || r.OK {
		t.Fatalf("unknown runtime: %d %+v", code, r)
	}
	code, r = postChat(t, ts, "/api/chat/operator-api/runtime", map[string]any{"runtime": "claude"})
	if code != http.StatusOK || r.Status.Runtime != chat.RuntimeClaude {
		t.Fatalf("back to claude: %d %+v", code, r)
	}
}

// An interrupt with "by" is an agent's mail and gets the labelled note; one
// without it is the app's stop button, the user's, and stays a plain interrupt.
func TestChatAPIInterruptLabelsOnlyAnAgentsInterrupt(t *testing.T) {
	_, ts := newChatServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	notePath := filepath.Join(os.Getenv(chattest.DirVar), "interrupt-note.json")

	ws := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/chat/operator-api/events?since=0&token=" + testToken
	c, _, err := websocket.Dial(ctx, ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	readUntil(t, ctx, c, chat.KindReplayDone)

	cut := func(body map[string]any) []chat.Event {
		t.Helper()
		code, r := postChat(t, ts, "/api/chat/operator-api/send", map[string]any{"text": "long story", "start": true})
		if code != http.StatusOK || !r.OK {
			t.Fatalf("send: %d %+v", code, r)
		}
		readUntil(t, ctx, c, chat.KindTextDelta)
		if code, r := postChat(t, ts, "/api/chat/operator-api/interrupt", body); code != http.StatusOK || !r.OK {
			t.Fatalf("interrupt %v: %d %+v", body, code, r)
		}
		return readUntil(t, ctx, c, chat.KindResult)
	}

	cut(nil)
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(notePath); err == nil {
		t.Fatal("the user's stop (no by) got a Commander note")
	}

	seen := cut(map[string]any{"by": "commander"})
	note := ""
	for note == "" { // the note may land before or after the cut turn's result
		for _, ev := range seen {
			if ev.Kind == chat.KindUser && note == "" {
				note = ev.Text
			}
		}
		if note == "" {
			seen = readUntil(t, ctx, c, chat.KindUser)
		}
	}
	if !strings.HasPrefix(note, "[commander interrupt] commander ") {
		t.Fatalf("by=commander: first message after the cut = %q, want the labelled note", note)
	}
}
