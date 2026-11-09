package agentmail

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeServer returns an httptest server that answers tools/call by looking up
// tool name in responses. The value is the JSON text placed in content[0].text.
// Prefix a value with "ERR:" to return isError=true with the rest as the text.
func fakeServer(t *testing.T, responses map[string]string) (*httptest.Server, *[]rpcRequest) {
	t.Helper()
	var seen []rpcRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req rpcRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("bad request body: %v", err)
		}
		seen = append(seen, req)
		text, ok := responses[req.Params.Name]
		if !ok {
			t.Errorf("unexpected tool call: %s", req.Params.Name)
		}
		isErr := false
		if after, found := strings.CutPrefix(text, "ERR:"); found {
			isErr, text = true, after
		}
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": text}},
				"isError": isErr,
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestServerClientRegisterAgent(t *testing.T) {
	srv, seen := fakeServer(t, map[string]string{
		"register_agent": `{"id":7,"name":"GoldTiger","program":"opencode-ai","model":"sonnet"}`,
	})
	c := NewServerClient(srv.URL, "tok")

	name, err := c.RegisterAgent(context.Background(), "/abs/proj", "opencode-ai", "sonnet", "manager work")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	if name != "GoldTiger" {
		t.Errorf("name = %q, want GoldTiger", name)
	}
	// it must NOT send a "name" argument (server assigns it)
	args := (*seen)[0].Params.Arguments
	if _, ok := args["name"]; ok {
		t.Errorf("RegisterAgent must not pass a name; args=%v", args)
	}
	if args["project_key"] != "/abs/proj" {
		t.Errorf("project_key = %v, want /abs/proj", args["project_key"])
	}
}

func TestServerClientRegisterAgentRoleNameRejected(t *testing.T) {
	srv, _ := fakeServer(t, map[string]string{
		"register_agent": "ERR:'manager-commander' looks like a descriptive role name.",
	})
	c := NewServerClient(srv.URL, "tok")
	_, err := c.RegisterAgent(context.Background(), "/abs/proj", "opencode-ai", "sonnet", "")
	if err == nil {
		t.Fatal("expected error for isError response")
	}
	if !strings.Contains(err.Error(), "descriptive role name") {
		t.Errorf("error should surface server message, got: %v", err)
	}
}

func TestServerClientSendMessage(t *testing.T) {
	srv, seen := fakeServer(t, map[string]string{
		"send_message": `{"id":99,"status":"sent"}`,
	})
	c := NewServerClient(srv.URL, "tok")
	err := c.SendMessage(context.Background(), "/abs/proj", "GoldTiger", []string{"BlueLake"}, "subj", "hello", SendOptions{Importance: "high", AckRequired: true})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	args := (*seen)[0].Params.Arguments
	if args["sender_name"] != "GoldTiger" {
		t.Errorf("sender_name = %v", args["sender_name"])
	}
	if args["body_md"] != "hello" {
		t.Errorf("body_md = %v (must map body -> body_md)", args["body_md"])
	}
	if args["importance"] != "high" || args["ack_required"] != true {
		t.Errorf("opts not mapped: %v", args)
	}
	// Role mail leaves the contact fallback to the server.
	if _, sent := args["auto_contact_if_blocked"]; sent {
		t.Errorf("auto_contact_if_blocked sent without NoAutoContact: %v", args)
	}
	// `to` must serialize as a JSON array
	to, ok := args["to"].([]any)
	if !ok || len(to) != 1 || to[0] != "BlueLake" {
		t.Errorf("to = %v, want [BlueLake]", args["to"])
	}
}

func TestServerClientSendMessageNoAutoContact(t *testing.T) {
	srv, seen := fakeServer(t, map[string]string{"send_message": `{}`})
	c := NewServerClient(srv.URL, "tok")
	if err := c.SendMessage(context.Background(), "/abs/proj", "GoldTiger", []string{"BlueLake"}, "subj", "hello", SendOptions{NoAutoContact: true}); err != nil {
		t.Fatal(err)
	}
	if v, sent := (*seen)[0].Params.Arguments["auto_contact_if_blocked"]; !sent || v != false {
		t.Errorf("auto_contact_if_blocked=%v sent=%v, want explicit false", v, sent)
	}
}

func TestServerClientFetchInbox(t *testing.T) {
	srv, seen := fakeServer(t, map[string]string{
		"fetch_inbox": `[{"id":12,"subject":"hi","from":"BlueLake","body_md":"yo","importance":"normal","created_ts":"2026-05-25T05:08:25Z"}]`,
	})
	c := NewServerClient(srv.URL, "tok")
	mail, err := c.FetchInbox(context.Background(), "/abs/proj", "GoldTiger", time.Time{}, InboxOptions{IncludeBodies: true})
	if err != nil {
		t.Fatalf("FetchInbox: %v", err)
	}
	if len(mail) != 1 {
		t.Fatalf("got %d messages, want 1", len(mail))
	}
	m := mail[0]
	if m.ID != "12" || m.From != "BlueLake" || m.Subject != "hi" || m.Body != "yo" {
		t.Errorf("mapped mail wrong: %+v", m)
	}
	if m.SentAt.IsZero() {
		t.Errorf("created_ts not parsed into SentAt")
	}
	// A poll must be a pure read: the server's default marks mail read (a write).
	if got := (*seen)[0].Params.Arguments["mark_read"]; got != false {
		t.Errorf("mark_read = %v, want false", got)
	}
}

func TestServerClientListAgents(t *testing.T) {
	srv, _ := fakeServer(t, map[string]string{
		"list_agents": `[{"name":"GoldTiger","program":"opencode-ai","model":"sonnet","task_description":"x"}]`,
	})
	c := NewServerClient(srv.URL, "tok")
	agents, err := c.ListAgents(context.Background(), "/abs/proj")
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 || agents[0].Name != "GoldTiger" || agents[0].Program != "opencode-ai" {
		t.Errorf("agents wrong: %+v", agents)
	}
}

func TestServerClientSendsBearerToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"cmdr","result":{"content":[{"type":"text","text":"[]"}],"isError":false}}`))
	}))
	defer srv.Close()
	c := NewServerClient(srv.URL, "sekret")
	if _, err := c.ListAgents(context.Background(), "/p"); err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if gotAuth != "Bearer sekret" {
		t.Errorf("Authorization = %q, want 'Bearer sekret'", gotAuth)
	}
}

// TestServerClientLive hits a real am server when one is reachable. Skips
// otherwise so unit runs stay hermetic. Enable with AM_LIVE_TEST=1.
func TestServerClientLive(t *testing.T) {
	if os.Getenv("AM_LIVE_TEST") == "" {
		t.Skip("set AM_LIVE_TEST=1 to run against a live am server")
	}
	c, err := NewServerClientFromEnv()
	if err != nil {
		t.Fatalf("NewServerClientFromEnv: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	slug, err := c.EnsureProject(ctx, "/tmp/example/code/commander")
	if err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	if slug == "" {
		t.Fatal("EnsureProject returned empty slug")
	}
	if _, err := c.ListAgents(ctx, "/tmp/example/code/commander"); err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
}
