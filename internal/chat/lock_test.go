package chat

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/store"
)

func writeFile(path, body string) error { return os.WriteFile(path, []byte(body), 0o644) }

func TestParsePS(t *testing.T) {
	id := "00000000-0000-4000-8000-000000000010"
	out := "  101 /bin/zsh\n" +
		"  202 claude -n manager-x --dangerously-skip-permissions --resume " + id + "\n" +
		"  303 sh -c DISABLE_AUTOUPDATER=1 claude -n commander --session-id " + id + "\n" +
		"  404 claude -p --input-format stream-json --resume=" + id + " --verbose\n" +
		"  505 claude --resume " + id + "0\n" + // a different id with our id as prefix
		"  606 grep " + id + "\n" +
		"  707 tmux new-session -d -s commander claude -n commander --resume " + id + "\n"
	procs := parsePS(out, id)
	var pids []int
	for _, p := range procs {
		pids = append(pids, p.PID)
	}
	if len(pids) != 3 || pids[0] != 202 || pids[1] != 303 || pids[2] != 404 {
		t.Fatalf("pids = %v", pids)
	}
	if isHeadless(procs[0].Command) || !isHeadless(procs[2].Command) {
		t.Fatal("headless detection")
	}
	if o := foreignOwner(id, procs, map[int]bool{202: true, 303: true}); o == nil || o.PID != 404 || o.By != "headless" {
		t.Fatalf("foreign = %+v", o)
	}
}

// TestPSScannerFindsARealProcess runs the real ps against a dummy process whose
// command line carries --resume <id>, like a tmux TUI claude.
func TestPSScannerFindsARealProcess(t *testing.T) {
	id := agentrt.NewClaudeSessionID()
	cmd := exec.Command("sh", "-c", "sleep 5; true", "claude", "--resume", id)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	// Under load the forked sh can show up twice (fork before exec); what
	// matters is that the one we started is found.
	var procs []Proc
	found := func() bool {
		for _, p := range procs {
			if p.PID == cmd.Process.Pid {
				return true
			}
		}
		return false
	}
	for i := 0; i < 250 && !found(); i++ {
		var err error
		if procs, err = PSScanner(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !found() {
		t.Fatalf("procs = %+v, want pid %d", procs, cmd.Process.Pid)
	}
	if o := foreignOwner(id, procs, nil); o == nil || o.By != "terminal" {
		t.Fatalf("owner = %+v", o)
	}
}

func TestSessionLockExclusive(t *testing.T) {
	dir := t.TempDir()
	a, err := acquireSessionLock(dir, "s1", "commander")
	if err != nil {
		t.Fatal(err)
	}
	_, err = acquireSessionLock(dir, "s1", "other")
	if o, ok := err.(*OwnedError); !ok || o.By != "lock" || !strings.Contains(o.Detail, "role=commander") {
		t.Fatalf("second lock: %v", err)
	}
	if _, err := acquireSessionLock(dir, "s2", "other"); err != nil {
		t.Fatalf("other session: %v", err)
	}
	a.release()
	b, err := acquireSessionLock(dir, "s1", "other")
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	b.release()
}

func TestStateTracker(t *testing.T) {
	tr := newStateTracker()
	step := func(e Event, want string) {
		t.Helper()
		if e.Source == "" {
			e.Source = SourceLive
		}
		tr.observe(e)
		got := tr.state()
		if strings.TrimSpace(got.State+" "+got.Detail) != want {
			t.Fatalf("after %s: %q, want %q", e.Kind, got.State+" "+got.Detail, want)
		}
	}
	step(Event{Kind: KindSession}, store.AgentIdle)
	step(Event{Kind: KindUser, Origin: "user", Source: SourceEngine}, store.AgentWorking)
	step(Event{Kind: KindToolInput, Tool: &Tool{Name: "Bash", Input: []byte(`{"command":"go   test ./..."}`)}}, "working Bash: go test ./...")
	step(Event{Kind: KindPermission, Request: &Request{ID: "r1", ToolName: "Bash"}}, "waiting needs you: approve Bash")
	step(Event{Kind: KindResolved, Request: &Request{ID: "r1"}}, "working Bash: go test ./...")
	step(Event{Kind: KindToolResult, Tool: &Tool{}}, store.AgentWorking)
	step(Event{Kind: KindTask, Subtype: "task_started", Task: &Task{TaskID: "t1", Description: "Wait for #4110 CI to finish"}}, store.AgentWorking)
	step(Event{Kind: KindResult, Result: &Result{}}, "waiting #4110 CI to finish")
	step(Event{Kind: KindTask, Subtype: "task_notification", Task: &Task{TaskID: "t1", Status: "completed"}}, store.AgentIdle)
	step(Event{Kind: KindUser, Origin: "transcript", Source: SourceTranscript}, store.AgentIdle)
	step(Event{Kind: KindUser, Origin: "mail", Source: SourceEngine}, store.AgentWorking)
	step(Event{Kind: KindQuestion, Request: &Request{ID: "q", ToolName: "AskUserQuestion"}}, "waiting needs you: question")
	step(Event{Kind: KindExit, Exit: &Exit{Code: 1}}, "stuck process exited mid-turn (code 1)")
	step(Event{Kind: KindSession}, store.AgentIdle)
	step(Event{Kind: KindExit, Exit: &Exit{Expected: true}}, store.AgentIdle)
}

func TestValidRole(t *testing.T) {
	for _, r := range []string{"commander", "manager-example-app-t11", "operator-feat-x.y_z"} {
		if !ValidRole(r) {
			t.Errorf("%s rejected", r)
		}
	}
	for _, r := range []string{"", "manager-", "../x", "manager-../../etc", "operator-a/b", "lead", "commander2"} {
		if ValidRole(r) {
			t.Errorf("%s accepted", r)
		}
	}
}

func TestArgsResumeAndSkip(t *testing.T) {
	a := strings.Join(Args(Spec{SessionID: "id", SkipPermissions: true, AllowedTools: "x"}, true), " ")
	if !strings.HasSuffix(a, "--resume id") || !strings.Contains(a, "--dangerously-skip-permissions") || strings.Contains(a, "--allowedTools") {
		t.Fatalf("args = %s", a)
	}
}

func TestArgsManagerDisallowsEditTools(t *testing.T) {
	mgr := strings.Join(Args(Spec{Agent: "manager-ws", SessionID: "id"}, true), " ")
	if !strings.Contains(mgr, "--disallowedTools mcp__mcp-agent-mail,mcp__mcp_agent_mail,Edit,Write,MultiEdit,NotebookEdit ") {
		t.Errorf("manager args = %s", mgr)
	}
	op := strings.Join(Args(Spec{Agent: "operator-x", SessionID: "id"}, true), " ")
	if strings.Contains(op, ",Edit,") {
		t.Errorf("operator args = %s", op)
	}
}
