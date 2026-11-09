package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/boardturn"
	"github.com/example-user/commander/internal/mailstate"
	"github.com/example-user/commander/internal/store"
)

// useInbox points the mail deps at an in-memory inbox for role "manager-x".
func useInbox(t *testing.T, mails ...agentmail.Mail) {
	t.Helper()
	t.Setenv("COMMANDER_HOME", t.TempDir())
	st := newFakeAmStore()
	st.m[amKey+"\x00manager-x"] = "CalmCave"
	m := &fakeMailer{inbox: mails}
	orig := mailDeps
	mailDeps = func() (amAgentStore, amMailer, string, func(), error) { return st, m, amKey, func() {}, nil }
	t.Cleanup(func() { mailDeps = orig })
}

func freshMail(id, subject, body string) agentmail.Mail {
	return agentmail.Mail{ID: id, From: "BoldFox", Subject: subject, Body: body, SentAt: time.Now()}
}

func runHook(t *testing.T, stdin string, args ...string) (string, int) {
	t.Helper()
	var out bytes.Buffer
	code := runInternalMailHook(args, strings.NewReader(stdin), &out)
	return out.String(), code
}

func TestMailHook_UserPromptSubmitInjectsUnreadOnceAndAdvancesCursor(t *testing.T) {
	useInbox(t, freshMail("1", "Plan  ready", "see\nthe plan"))

	out, code := runHook(t, "", "--role", "manager-x", "--event", "UserPromptSubmit")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var got struct {
		Hook struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not json: %q: %v", out, err)
	}
	if got.Hook.Event != "UserPromptSubmit" {
		t.Fatalf("event = %q", got.Hook.Event)
	}
	for _, want := range []string{"[mail] from BoldFox: Plan ready", "> see\n> the plan", "did not type this"} {
		if !strings.Contains(got.Hook.Context, want) {
			t.Fatalf("context missing %q:\n%s", want, got.Hook.Context)
		}
	}

	out, code = runHook(t, "", "--role", "manager-x", "--event", "UserPromptSubmit")
	if code != 0 || strings.Contains(out, "[mail]") || !strings.Contains(out, "board is unavailable") {
		t.Fatalf("second run must carry only the board, got %q (exit %d)", out, code)
	}
}

func TestMailHook_StopBlocksWithMailAsReason(t *testing.T) {
	useInbox(t, freshMail("1", "urgent", "do it"))
	out, _ := runHook(t, "", "--role", "manager-x", "--event", "Stop")
	var got struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not json: %q: %v", out, err)
	}
	if got.Decision != "block" || !strings.Contains(got.Reason, "[mail] from BoldFox: urgent") {
		t.Fatalf("got %+v", got)
	}
}

func TestMailHook_CommanderStopAsksForTheWholeAwayReport(t *testing.T) {
	useInbox(t, freshMail("1", "urgent", "do it"))
	out, _ := runHook(t, "", "--role", "commander", "--event", "Stop")
	if !strings.Contains(out, "What changed:") || !strings.Contains(out, "Never reply with only the new items") {
		t.Fatalf("commander mail lacks the away-report reminder: %q", out)
	}
	useInbox(t, freshMail("2", "urgent", "do it"))
	if out, _ := runHook(t, "", "--role", "manager-x", "--event", "Stop"); strings.Contains(out, "What changed:") {
		t.Fatalf("a Manager got the away-report reminder: %q", out)
	}
}

func TestMailHook_PostToolUseFlagsAFailedCmdrWrite(t *testing.T) {
	in := `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"cmdr internal-set-objective --bead b --waiting someone-hold 2>&1 | tail -1"},` +
		`"tool_response":{"stdout":"banner\n{\"error\":\"--waiting \\\"someone-hold\\\": kind must be ci\"}","stderr":""}}`
	out, _ := runHook(t, in, "--role", "commander")
	if !strings.Contains(out, `"decision":"block"`) || !strings.Contains(out, "STATE NOT CHANGED") || !strings.Contains(out, "kind must be ci") {
		t.Fatalf("a failed write was not flagged: %q", out)
	}
	ok := `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"cmdr internal-set-objective --bead b"},"tool_response":{"stdout":"{\"bead_id\":\"b\"}"}}`
	if out, _ := runHook(t, ok, "--role", "commander"); strings.TrimSpace(out) != "" {
		t.Fatalf("a successful write was flagged: %q", out)
	}
}

func TestMailHook_EventFallsBackToStdin(t *testing.T) {
	useInbox(t, freshMail("1", "s", "b"))
	out, _ := runHook(t, `{"hook_event_name":"Stop","session_id":"x"}`, "--role", "manager-x")
	if !strings.Contains(out, `"decision":"block"`) {
		t.Fatalf("stdin event ignored: %q", out)
	}
}

func TestMailHook_SilentWhenNothingUnread(t *testing.T) {
	useInbox(t)
	if out, code := runHook(t, "", "--role", "manager-x", "--event", "Stop"); out != "" || code != 0 {
		t.Fatalf("got %q exit %d", out, code)
	}
}

func TestMailHook_NeverFailsTheHook(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	orig := mailDeps
	mailDeps = func() (amAgentStore, amMailer, string, func(), error) {
		return nil, nil, "", nil, errors.New("no am bearer token")
	}
	t.Cleanup(func() { mailDeps = orig })

	for _, args := range [][]string{
		{"--role", "manager-x", "--event", "Stop"}, // mail server unavailable
		{},                         // missing role
		{"--bogus"},                // bad flag
		{"--role", "../../escape"}, // unusable role
	} {
		if out, code := runHook(t, "", args...); out != "" || code != 0 {
			t.Fatalf("args %v: got %q exit %d, want silent exit 0", args, out, code)
		}
	}
}

func TestMailHook_CapsBacklogAndLeavesRestUnread(t *testing.T) {
	var mails []agentmail.Mail
	for i := 1; i <= maxHookMails+3; i++ {
		mails = append(mails, freshMail(fmt.Sprint(i), fmt.Sprintf("m%d", i), "b"))
	}
	useInbox(t, mails...)

	out, _ := runHook(t, "", "--role", "manager-x", "--event", "UserPromptSubmit")
	if !strings.Contains(out, "m10") || strings.Contains(out, "m11") || !strings.Contains(out, "3 more unread") {
		t.Fatalf("cap not applied: %s", out)
	}
	out, _ = runHook(t, "", "--role", "manager-x", "--event", "UserPromptSubmit")
	if !strings.Contains(out, "m11") || !strings.Contains(out, "m13") {
		t.Fatalf("remaining mail must come on the next run: %s", out)
	}
}

func TestRunInternalInbox_UnreadAndMarkRead(t *testing.T) {
	useInbox(t, freshMail("1", "a", "b"), freshMail("2", "c", "d"))

	run := func(args ...string) []agentmail.Mail {
		t.Helper()
		stdout, stderr, readOutput := withTempOutputFiles(t)
		if code := runInternalInbox(args, stdout, stderr); code != 0 {
			_, errOut := readOutput()
			t.Fatalf("exit=%d stderr=%s", code, errOut)
		}
		out, _ := readOutput()
		var mails []agentmail.Mail
		if err := json.Unmarshal([]byte(out), &mails); err != nil {
			t.Fatalf("bad json %q: %v", out, err)
		}
		return mails
	}

	if got := run("--agent", "manager-x", "--unread"); len(got) != 2 {
		t.Fatalf("unread = %d", len(got))
	}
	if got := run("--agent", "manager-x", "--unread"); len(got) != 2 {
		t.Fatalf("--unread alone must not advance the cursor, got %d", len(got))
	}
	if got := run("--agent", "manager-x", "--unread", "--mark-read"); len(got) != 2 {
		t.Fatalf("mark-read run still prints the mail, got %d", len(got))
	}
	if got := run("--agent", "manager-x", "--unread"); len(got) != 0 {
		t.Fatalf("after mark-read unread = %d", len(got))
	}
	if got := run("--agent", "manager-x"); len(got) != 2 {
		t.Fatalf("plain inbox still shows everything, got %d", len(got))
	}
}

func guardInput(command, cwd string) string {
	b, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": cwd, "tool_input": map[string]any{"command": command}})
	return string(b)
}

func TestManagerGuard_DeniesLogsNotesAndMailsOncePer30Min(t *testing.T) {
	useInbox(t)
	home := os.Getenv("COMMANDER_HOME")
	st := newFakeAmStore()
	st.m[amKey+"\x00manager-x"] = "CalmCave"
	st.m[amKey+"\x00commander"] = "BoldFox"
	m := &fakeMailer{}
	mailDeps = func() (amAgentStore, amMailer, string, func(), error) { return st, m, amKey, func() {}, nil }

	var out bytes.Buffer
	if code := runInternalManagerGuard([]string{"--role", "manager-x"}, strings.NewReader(guardInput("git commit -m x", "/repo")), &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var got struct {
		H struct {
			Decision string `json:"permissionDecision"`
			Reason   string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.H.Decision != "deny" || !strings.HasPrefix(got.H.Reason, "Managers do not do the work.") {
		t.Fatalf("output = %q (%v)", out.String(), err)
	}
	runInternalManagerGuard([]string{"--role", "manager-x"}, strings.NewReader(guardInput("go test ./...", "/repo")), io.Discard)

	log, _ := os.ReadFile(filepath.Join(home, "logs", "manager-refusals.jsonl"))
	if n := strings.Count(string(log), "\n"); n != 2 || !strings.Contains(string(log), `"role":"manager-x"`) {
		t.Fatalf("refusal log = %q", log)
	}
	if len(m.sent) != 1 || !strings.Contains(m.sent[0].subject, "Manager manager-x tried to do work: git commit -m x") {
		t.Fatalf("mail = %+v", m.sent)
	}
	s, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	states, _ := s.ListAgentStates(context.Background())
	if len(states) != 1 || states[0].Role != "manager-x" || states[0].Detail != "⚠ Manager doing work" {
		t.Fatalf("agent state = %+v", states)
	}
}

func TestManagerGuard_AllowsAndFailsOpen(t *testing.T) {
	useInbox(t)
	for name, tc := range map[string]struct {
		args  []string
		stdin string
	}{
		"read-only":     {[]string{"--role", "manager-x"}, guardInput("git status && br list", "/repo")},
		"bad json":      {[]string{"--role", "manager-x"}, "{not json"},
		"empty":         {[]string{"--role", "manager-x"}, ""},
		"not a manager": {[]string{"--role", "operator-x"}, guardInput("git commit", "/repo")},
		"no role":       {nil, guardInput("git commit", "/repo")},
		"non-bash tool": {[]string{"--role", "manager-x"}, `{"tool_name":"Read","tool_input":{"file_path":"x"}}`},
	} {
		var out bytes.Buffer
		if code := runInternalManagerGuard(tc.args, strings.NewReader(tc.stdin), &out); code != 0 || out.Len() != 0 {
			t.Errorf("%s: exit %d output %q, want silent allow", name, code, out.String())
		}
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("COMMANDER_HOME"), "logs", "manager-refusals.jsonl")); err == nil {
		t.Error("allowed commands must not log refusals")
	}
}

// The hook runs at every prompt and stop of every agent: with no mail
// signalled since its last read it must not open the mail deps or call am.
func TestMailHook_IdleRunsSkipMailDepsAndMailServer(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	st := newFakeAmStore()
	st.m[amKey+"\x00manager-x"] = "CalmCave"
	m := &fakeMailer{}
	deps := 0
	orig := mailDeps
	mailDeps = func() (amAgentStore, amMailer, string, func(), error) { deps++; return st, m, amKey, func() {}, nil }
	t.Cleanup(func() { mailDeps = orig })

	for i := 0; i < 20; i++ {
		if out, _ := runHook(t, "", "--role", "manager-x", "--event", "Stop"); out != "" {
			t.Fatalf("empty inbox printed %q", out)
		}
	}
	if deps != 1 || len(m.fetchOpts) != 1 {
		t.Fatalf("20 idle runs: %d store opens, %d inbox reads; want 1, 1", deps, len(m.fetchOpts))
	}

	m.inbox = []agentmail.Mail{freshMail("1", "go", "now")}
	mailstate.Signal("manager-x")
	out, _ := runHook(t, "", "--role", "manager-x", "--event", "Stop")
	if !strings.Contains(out, "go") || len(m.fetchOpts) != 2 {
		t.Fatalf("signalled mail: out %q after %d reads", out, len(m.fetchOpts))
	}
	runHook(t, "", "--role", "manager-x", "--event", "Stop")
	if len(m.fetchOpts) != 2 {
		t.Fatalf("delivered mail read again: %d reads", len(m.fetchOpts))
	}
}

func TestMailHook_BacklogPastTheCapIsReadAgainWithoutASignal(t *testing.T) {
	var mails []agentmail.Mail
	for i := 1; i <= maxHookMails+2; i++ {
		mails = append(mails, freshMail(fmt.Sprint(i), "s", "b"))
	}
	useInbox(t, mails...)
	runHook(t, "", "--role", "manager-x", "--event", "Stop")
	out, _ := runHook(t, "", "--role", "manager-x", "--event", "Stop")
	if !strings.Contains(out, "[mail] from") {
		t.Fatalf("the rest of the backlog was not delivered: %q", out)
	}
}

func TestMailHook_SignedOutServerAsksForASignInCard(t *testing.T) {
	in := hookInput{Event: hookPostToolUse, ToolName: "mcp__notion__authenticate"}
	manager := signedOut(in, "manager-sample-svc-k3p2")
	for _, want := range []string{"notion is signed out", "--state needs-user", "Sign notion in: run /mcp", "fresh Operator"} {
		if !strings.Contains(manager, want) {
			t.Errorf("manager message missing %q: %s", want, manager)
		}
	}
	if op := signedOut(in, "operator-x"); !strings.Contains(op, "report to your Manager") || strings.Contains(op, "--state") {
		t.Errorf("operator message: %s", op)
	}
	if got := signedOut(hookInput{ToolName: "mcp__claude_ai_Notion__authenticate"}, "commander"); !strings.Contains(got, "Notion is signed out") {
		t.Errorf("connector name: %s", got)
	}
	if signedOut(hookInput{ToolName: "mcp__notion__notion-fetch"}, "commander") != "" {
		t.Error("an ordinary tool call is not a sign-in")
	}

	var stdout strings.Builder
	runInternalMailHook([]string{"--role", "operator-x", "--event", hookPostToolUse}, strings.NewReader(`{"tool_name":"mcp__notion__authenticate"}`), &stdout)
	if !strings.Contains(stdout.String(), `"decision":"block"`) {
		t.Errorf("hook output: %s", stdout.String())
	}
}

// writeBoard publishes a board for role as cmdrd would.
func writeBoard(t *testing.T, role string, es ...boardturn.Entry) {
	t.Helper()
	(&boardturn.Writer{Home: os.Getenv("COMMANDER_HOME")}).Write(map[string][]boardturn.Entry{role: es}, time.Now())
}

func boardEntry(id, kind, text string) boardturn.Entry {
	return boardturn.Entry{ID: id, Label: "#" + id, Kind: kind, Text: text, Key: text, BeadID: "ws" + id}
}

func TestMailHook_EveryPromptCarriesTheBoardEvenWhileTheUserTalks(t *testing.T) {
	useInbox(t, freshMail("1", "held", "for later"))
	home := os.Getenv("COMMANDER_HOME")
	writeBoard(t, "commander", boardEntry("1", "working", "Ship it: Working"), boardEntry("2", "stalled", "Docs: Stalled"))
	if err := os.MkdirAll(filepath.Join(home, "chat"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "chat", "commander.talking"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out, _ := runHook(t, "", "--role", "commander", "--event", "UserPromptSubmit")
	if !strings.Contains(out, "#2 Docs: Stalled") || strings.Contains(out, "[mail]") {
		t.Fatalf("talking turn: want the board and no mail, got %q", out)
	}
	out, _ = runHook(t, "", "--role", "commander", "--event", "UserPromptSubmit")
	if !strings.Contains(out, "v1: unchanged since you last saw it") {
		t.Fatalf("second turn: %q", out)
	}
	// A Stop without mail is never blocked for the board.
	if out, _ := runHook(t, `{"hook_event_name":"Stop","last_assistant_message":"Working on it."}`, "--role", "commander"); out != "" {
		t.Fatalf("stop without mail printed %q", out)
	}
	// SessionStart (a compaction) makes the next one full again.
	runHook(t, `{"hook_event_name":"SessionStart","source":"compact"}`, "--role", "commander")
	if out, _ := runHook(t, "", "--role", "commander", "--event", "UserPromptSubmit"); !strings.Contains(out, "exactly as the user's sidebar shows it") {
		t.Fatalf("after compaction: %q", out)
	}
}

func TestMailHook_StopWithMailCarriesTheBoardDiff(t *testing.T) {
	useInbox(t)
	rest := []boardturn.Entry{boardEntry("2", "working", "B: Working"), boardEntry("3", "working", "C: Working"), boardEntry("4", "working", "D: Working")}
	writeBoard(t, "manager-x", append([]boardturn.Entry{boardEntry("1", "working", "Ship it: Working")}, rest...)...)
	runHook(t, "", "--role", "manager-x", "--event", "UserPromptSubmit")
	writeBoard(t, "manager-x", append([]boardturn.Entry{boardEntry("1", "waiting", "Ship it: Waiting for CI")}, rest...)...)
	useInboxKeepHome(t, freshMail("1", "ci", "started"))
	out, _ := runHook(t, "", "--role", "manager-x", "--event", "Stop")
	if !strings.Contains(out, `"decision":"block"`) || !strings.Contains(out, "[mail] from BoldFox: ci") ||
		!strings.Contains(out, "since v1, which you saw") || !strings.Contains(out, "~ #1 Ship it: Waiting for CI") {
		t.Fatalf("got %q", out)
	}
}

func TestMailHook_CommanderStopRefusesAFixedClaimOnAStalledRowOnce(t *testing.T) {
	useInbox(t)
	writeBoard(t, "commander", boardEntry("3", "stalled", "Docs: State missing"))
	in := `{"hook_event_name":"Stop","last_assistant_message":"All set: #3 is fixed and the board is accurate."}`
	out, _ := runHook(t, in, "--role", "commander")
	if !strings.Contains(out, `"decision":"block"`) || !strings.Contains(out, `#3 reads \"Docs: State missing\". Your reply says it is fixed`) {
		t.Fatalf("got %q", out)
	}
	again := `{"hook_event_name":"Stop","stop_hook_active":true,"last_assistant_message":"#3 is fixed."}`
	if out, _ := runHook(t, again, "--role", "commander"); out != "" {
		t.Fatalf("blocked twice: %q", out)
	}
	if out, _ := runHook(t, in, "--role", "manager-x"); out != "" {
		t.Fatalf("a Manager's reply was checked: %q", out)
	}
	// From the transcript when the input has no copy of the reply.
	tr := filepath.Join(t.TempDir(), "t.jsonl")
	_ = os.WriteFile(tr, []byte(`{"type":"user","message":{"content":"is it fixed"}}`+"\n"+
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Yes, #3 is resolved."}]}}`+"\n"), 0o600)
	if out, _ := runHook(t, `{"hook_event_name":"Stop","transcript_path":"`+tr+`"}`, "--role", "commander"); !strings.Contains(out, "says it is resolved") {
		t.Fatalf("transcript reply not checked: %q", out)
	}
}

func TestMailHook_PostToolUseShowsTheRowAWriteChanged(t *testing.T) {
	useInbox(t)
	writeBoard(t, "commander", boardEntry("3", "waiting", "Docs: Waiting for review"))
	in := `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"cmdr internal-set-objective --project p --bead ws3.2 --state in-progress"},"tool_response":{"stdout":"{}"}}`
	out, _ := runHook(t, in, "--role", "commander")
	if !strings.Contains(out, `"hookEventName":"PostToolUse"`) || !strings.Contains(out, `board v1 reads:\n#3 Docs: Waiting for review`) {
		t.Fatalf("got %q", out)
	}
}

// useInboxKeepHome swaps the inbox without a new COMMANDER_HOME.
func useInboxKeepHome(t *testing.T, mails ...agentmail.Mail) {
	t.Helper()
	home := os.Getenv("COMMANDER_HOME")
	useInbox(t, mails...)
	t.Setenv("COMMANDER_HOME", home)
	mailstate.Signal("manager-x")
}

// A fresh, resumed or compacted Commander session opens with the conversation
// as the user saw it in the app; no other role gets it, and no other event.
func TestMailHook_CommanderSessionStartCarriesWhatTheUserSaw(t *testing.T) {
	useInbox(t)
	home := os.Getenv("COMMANDER_HOME")
	log := `{"seq":1,"role":"commander","kind":"user_message","time":"2026-10-28T09:00:00Z","source":"engine","origin":"user","text":"Ship the docs fix."}
{"seq":2,"role":"commander","kind":"tool_result","time":"2026-10-28T09:00:01Z","source":"live","tool":{"id":"t1","content":"tool output"}}
{"seq":3,"role":"commander","kind":"turn_result","time":"2026-10-28T09:00:02Z","source":"live","result":{"subtype":"success","text":"The docs fix is shipped.","cost_delta_usd":0,"total_cost_usd":0}}
`
	for _, role := range []string{"commander", "manager-x"} {
		if err := os.MkdirAll(filepath.Join(home, "chat"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "chat", role+".jsonl"), []byte(log), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, source := range []string{"startup", "resume", "compact"} {
		out, code := runHook(t, `{"hook_event_name":"SessionStart","source":"`+source+`"}`, "--role", "commander")
		var got struct {
			Out struct {
				Event   string `json:"hookEventName"`
				Context string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if code != 0 || json.Unmarshal([]byte(out), &got) != nil || got.Out.Event != "SessionStart" ||
			!strings.HasPrefix(got.Out.Context, "Conversation so far as the user saw it") ||
			!strings.Contains(got.Out.Context, "User: Ship the docs fix.\nYou replied: The docs fix is shipped.") ||
			strings.Contains(got.Out.Context, "tool output") {
			t.Fatalf("%s: got %q", source, out)
		}
	}
	if out, _ := runHook(t, `{"hook_event_name":"SessionStart","source":"resume"}`, "--role", "manager-x"); out != "" {
		t.Fatalf("manager got %q", out)
	}
	if out, _ := runHook(t, "", "--role", "commander", "--event", "UserPromptSubmit"); strings.Contains(out, "Conversation so far") {
		t.Fatalf("prompt carried the digest: %q", out)
	}
}

func TestMailHook_CommanderSessionStartWithoutAChatLogPrintsNothing(t *testing.T) {
	useInbox(t)
	if out, code := runHook(t, `{"hook_event_name":"SessionStart","source":"startup"}`, "--role", "commander"); out != "" || code != 0 {
		t.Fatalf("got %q (exit %d)", out, code)
	}
}
