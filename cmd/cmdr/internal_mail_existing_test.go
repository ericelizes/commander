package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/agentmail"
)

// existingAgentServer is a synthetic mail server: GoldTiger and BlueLake exist
// unless a whois override says otherwise, and every other tool is refused.
type existingAgentServer struct {
	recipient, senderWhois, sendReply string
	wrongProject, sameID              bool
	calls                             []string
	send                              map[string]any
}

func (f *existingAgentServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params struct {
				Name      string
				Arguments map[string]any
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		f.calls = append(f.calls, req.Params.Name)
		args := req.Params.Arguments
		if args["project_key"] != amKey {
			t.Errorf("wrong project: %v", args)
		}
		payload := `{"id":1,"project_id":3,"name":"GoldTiger"}`
		switch req.Params.Name {
		case "whois":
			if args["include_recent_commits"] != false {
				t.Error("history enabled")
			}
			if args["agent_name"] == "BlueLake" {
				project, id := 3, 2
				if f.wrongProject {
					project = 4
				}
				if f.sameID {
					id = 1
				}
				payload = fmt.Sprintf(`{"id":%d,"project_id":%d,"name":"BlueLake"}`, id, project)
				if f.recipient != "" {
					payload = f.recipient
				}
			} else if f.senderWhois != "" {
				payload = f.senderWhois
			}
		case "send_message":
			f.send = args
			if args["sender_name"] != "GoldTiger" || fmt.Sprint(args["to"]) != "[BlueLake]" || args["body_md"] != "synthetic payload" {
				t.Errorf("send=%v", args)
			}
			payload = `{}`
			if f.sendReply != "" {
				payload = f.sendReply
			}
		default:
			t.Errorf("forbidden call: %s", req.Params.Name)
		}
		text, isErr := strings.CutPrefix(payload, "ERR:")
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "isError": isErr}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestMailExistingAgent(t *testing.T) {
	for _, tc := range []struct {
		name, recipient, senderWhois, sendReply, collision string
		senderMissing, wrongProject, sameID                bool
		want                                               bool
		wantErr                                            string
	}{
		{name: "exact", want: true},
		{name: "missing sender", senderMissing: true, wantErr: "resolve existing sender"},
		{name: "absent recipient", recipient: "ERR:Agent 'BlueLake' not found in project '/fixture/fleet'", wantErr: "verify recipient: agentmail: no existing agent"},
		{name: "normalized identity", recipient: `{"id":2,"project_id":3,"name":"Bluelake"}`, wantErr: "verify recipient: agentmail: no existing agent"},
		{name: "retired recipient", recipient: `{"id":2,"project_id":3,"name":"BlueLake","retired_at":"2026-01-01"}`, wantErr: "verify recipient: agentmail: existing agent is retired or deregistered"},
		{name: "retired sender", senderWhois: `{"id":1,"project_id":3,"name":"GoldTiger","retired_at":"2026-01-01"}`, wantErr: "verify sender: agentmail: existing agent is retired or deregistered"},
		{name: "whois failure", recipient: "ERR:database is busy", wantErr: "verify recipient: agentmail: could not read the identity"},
		{name: "unsupported server", recipient: "ERR:Unknown tool: whois", wantErr: "verify recipient: agentmail: mail server cannot verify existing identities"},
		{name: "incomplete profile", recipient: `{"name":"BlueLake"}`, wantErr: "verify recipient: agentmail: mail server cannot verify existing identities"},
		{name: "wrong project", wrongProject: true, wantErr: "conflicting server identity or project"},
		{name: "identity collision", sameID: true, wantErr: "conflicting server identity or project"},
		{name: "role collision", collision: "role", wantErr: "collides with registered role"},
		{name: "name collision", collision: "name", wantErr: "collides with registered role"},
		{name: "contact required", sendReply: "ERR:CONTACT_REQUIRED: no approved contact link", wantErr: "CONTACT_REQUIRED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newFakeAmStore()
			if !tc.senderMissing {
				st.m[amKey+"\x00manager-fixture"] = "GoldTiger"
			}
			if tc.collision == "role" {
				st.m[amKey+"\x00bluelake"] = "RedFox"
			}
			if tc.collision == "name" {
				st.m[amKey+"\x00manager-peer"] = "bluelake"
			}
			before := fmt.Sprint(st.m)
			fake := &existingAgentServer{recipient: tc.recipient, senderWhois: tc.senderWhois, sendReply: tc.sendReply, wrongProject: tc.wrongProject, sameID: tc.sameID}
			srv := fake.start(t)
			orig := mailDeps
			mailDeps = func() (amAgentStore, amMailer, string, func(), error) {
				return st, agentmail.NewServerClient(srv.URL, "fixture"), amKey, func() {}, nil
			}
			t.Cleanup(func() { mailDeps = orig })
			stdout, stderr, read := withTempOutputFiles(t)
			code := runInternalMail([]string{"--from", "manager-fixture", "--to-existing-agent", "BlueLake", "--subject", "synthetic", "--body", "synthetic payload", "--ack", "--importance", "high"}, stdout, stderr)
			out, errOut := read()
			if (code == 0) != tc.want {
				t.Fatalf("exit=%d out=%s err=%s", code, out, errOut)
			}
			if tc.want && (!strings.Contains(out, `"to_existing_agent":"BlueLake"`) || strings.Join(fake.calls, ",") != "whois,whois,send_message") {
				t.Fatalf("out=%s calls=%v", out, fake.calls)
			}
			if !tc.want {
				if strings.Contains(out, "sent") {
					t.Fatalf("refusal reported a send: %s", out)
				}
				if !strings.Contains(errOut, tc.wantErr) {
					t.Fatalf("err=%s, want %q", errOut, tc.wantErr)
				}
				if tc.sendReply == "" && strings.Contains(strings.Join(fake.calls, ","), "send_message") {
					t.Fatalf("sent on refused identity: %v", fake.calls)
				}
			}
			if fake.send != nil {
				if v, ok := fake.send["auto_contact_if_blocked"]; !ok || v != false {
					t.Errorf("auto_contact_if_blocked=%v present=%v, want explicit false", v, ok)
				}
				if fake.send["ack_required"] != true || fake.send["importance"] != "high" {
					t.Errorf("send=%v", fake.send)
				}
			}
			if fmt.Sprint(st.m) != before {
				t.Fatal("registration changed")
			}
		})
	}
}

// setMailCaller pins who is running the command, whatever the suite inherited.
func setMailCaller(t *testing.T, role, managerRole string) {
	t.Helper()
	t.Setenv("COMMANDER_ROLE", role)
	t.Setenv("COMMANDER_MANAGER_ROLE", managerRole)
}

func TestMailExistingAgentInvalidFlagsBeforeDeps(t *testing.T) {
	opened := false
	orig := mailDeps
	mailDeps = func() (amAgentStore, amMailer, string, func(), error) {
		opened = true
		return nil, nil, "", nil, errors.New("synthetic: dependencies opened")
	}
	t.Cleanup(func() { mailDeps = orig })
	for _, tc := range []struct {
		name, role, managerRole, from, wantErr string
		extra                                  []string
	}{
		{name: "with to", extra: []string{"--to", "manager-peer"}, wantErr: "one exact name"},
		{name: "with interrupt", extra: []string{"--interrupt"}, wantErr: "one exact name"},
		{name: "two names", extra: []string{"--to-existing-agent", "BlueLake,RedFox"}, wantErr: "one exact name"},
		{name: "padded name", extra: []string{"--to-existing-agent", " BlueLake"}, wantErr: "one exact name"},
		{name: "operator caller", role: "operator-fixture-1", wantErr: "Operator"},
		{name: "dotted operator caller", role: "operator-fixture.1", wantErr: "Operator"},
		{name: "operator sender", from: "operator-fixture-1", wantErr: "Operator"},
		{name: "dotted operator sender", from: "operator-fixture.1", wantErr: "Operator"},
		{name: "operator as itself", role: "operator-fixture.1", from: "operator-fixture.1", wantErr: "Operator"},
		{name: "operator before its role was set", managerRole: "manager-fixture", wantErr: "Operator"},
		{name: "another manager", role: "manager-peer", wantErr: "manager-peer"},
		{name: "commander as a manager", role: "commander", wantErr: "commander"},
		{name: "manager as commander", role: "manager-fixture", from: "commander", wantErr: "manager-fixture"},
		{name: "unknown caller", role: "reviewer-fixture", from: "reviewer-fixture", wantErr: "Managers, Commander and the user"},
		{name: "unknown sender", from: "reviewer-fixture", wantErr: "Managers, Commander and the user"},
		{name: "operator in capitals", from: "Operator-fixture", wantErr: "Managers, Commander and the user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setMailCaller(t, tc.role, tc.managerRole)
			opened = false
			from := tc.from
			if from == "" {
				from = "manager-fixture"
			}
			stdout, stderr, read := withTempOutputFiles(t)
			args := append([]string{"--from", from, "--subject", "synthetic", "--to-existing-agent", "BlueLake"}, tc.extra...)
			if runInternalMail(args, stdout, stderr) == 0 {
				t.Fatalf("accepted %v", args)
			}
			if opened {
				t.Fatalf("opened dependencies for %v", args)
			}
			if _, errOut := read(); !strings.Contains(errOut, tc.wantErr) {
				t.Fatalf("err=%s, want %q", errOut, tc.wantErr)
			}
		})
	}
}

func TestMailExistingAgentAllowedCallers(t *testing.T) {
	for _, tc := range []struct{ name, role, from, binding string }{
		{name: "user without a role", from: "manager-fixture", binding: "manager-fixture"},
		{name: "user as commander", from: "commander", binding: "commander"},
		{name: "user as user", from: "user", binding: "user"},
		{name: "manager as itself", role: "manager-fixture", from: "manager-fixture", binding: "manager-fixture"},
		{name: "manager in the other spelling", role: "manager-fixture.1", from: "manager-fixture-1", binding: "manager-fixture-1"},
		{name: "commander as itself", role: "commander", from: "commander", binding: "commander"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setMailCaller(t, tc.role, "")
			st := newFakeAmStore()
			st.m[amKey+"\x00"+tc.binding] = "GoldTiger"
			fake := &existingAgentServer{}
			srv := fake.start(t)
			orig := mailDeps
			mailDeps = func() (amAgentStore, amMailer, string, func(), error) {
				return st, agentmail.NewServerClient(srv.URL, "fixture"), amKey, func() {}, nil
			}
			t.Cleanup(func() { mailDeps = orig })
			stdout, stderr, read := withTempOutputFiles(t)
			code := runInternalMail([]string{"--from", tc.from, "--to-existing-agent", "BlueLake", "--subject", "synthetic", "--body", "synthetic payload"}, stdout, stderr)
			out, errOut := read()
			if code != 0 || !strings.Contains(out, `"sent":true`) {
				t.Fatalf("exit=%d out=%s err=%s", code, out, errOut)
			}
		})
	}
}

// Compile-time checks that the concrete production dependencies expose the read.
var _ interface {
	LookupExistingAgent(context.Context, string, string) (agentmail.ExistingAgent, error)
} = (*agentmail.ServerClient)(nil)
