package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/store"
)

// tabEnv gives a COMMANDER_HOME with one project and one workstream Manager,
// and pins the caller's window.
func tabEnv(t *testing.T, callerWS string) *store.Store {
	t.Helper()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	pid, err := st.InsertProject(context.Background(), store.Project{Name: "example-app", Type: "code", Path: t.TempDir(), ActivatedAt: 1, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertWorkstreamManager(context.Background(), store.WorkstreamManager{ProjectID: pid, WorkstreamBeadID: "example-app-t11", SessionFile: "/x"}); err != nil {
		t.Fatal(err)
	}
	old := tabCallerWS
	tabCallerWS = func() string { return callerWS }
	t.Cleanup(func() { tabCallerWS = old })
	return st
}

func tab(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := runTab(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestTab_AddDefaultsToCallersWindow(t *testing.T) {
	st := tabEnv(t, "example-app-t11")
	code, out, errOut := tab("add", "--url", "https://github.com/o/r/pull/4109", "--focus")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, "added: PR #4109") {
		t.Errorf("out = %q", out)
	}
	p, _ := st.GetProjectByName(context.Background(), "example-app")
	tabs, _ := st.ListTabs(context.Background(), p.ID, "example-app-t11")
	if len(tabs) != 1 || tabs[0].AddedBy != "manager-example-app-t11" || tabs[0].Kind != "url" || tabs[0].RefreshSecs != 60 {
		t.Fatalf("tabs = %+v", tabs)
	}
	if active, _ := st.GetActiveTab(context.Background(), p.ID, "example-app-t11"); active != tabs[0].ID {
		t.Errorf("--focus did not activate: %d", active)
	}
	// Idempotent.
	code, out, _ = tab("add", "--url", "https://github.com/o/r/pull/4109")
	if code != 0 || !strings.Contains(out, "already there") {
		t.Errorf("second add: %d %q", code, out)
	}
	if tabs, _ := st.ListTabs(context.Background(), p.ID, "example-app-t11"); len(tabs) != 1 {
		t.Errorf("duplicate tab created: %+v", tabs)
	}
}

func TestTab_CommanderWindowAndExplicitFlags(t *testing.T) {
	st := tabEnv(t, "") // window 0
	if code, _, e := tab("add", "--cmd", "gh pr checks 1", "--title", "checks", "--refresh", "30s"); code != 0 {
		t.Fatalf("add cmd: %s", e)
	}
	tabs, _ := st.ListTabs(context.Background(), 0, "")
	if len(tabs) != 1 || tabs[0].Kind != "cmd" || tabs[0].RefreshSecs != 30 || tabs[0].AddedBy != "user" || tabs[0].Title != "checks" {
		t.Fatalf("commander tabs = %+v", tabs)
	}
	// --ws/--project override the caller's window; a step id resolves to its workstream.
	if code, _, e := tab("add", "--url", "https://example.com/spec", "--ws", "example-app-t11.1.2", "--project", "example-app"); code != 0 {
		t.Fatalf("add with flags: %s", e)
	}
	p, _ := st.GetProjectByName(context.Background(), "example-app")
	if tabs, _ := st.ListTabs(context.Background(), p.ID, "example-app-t11"); len(tabs) != 1 {
		t.Errorf("workstream tabs = %+v", tabs)
	}
	// --ws alone finds the project through the Manager row.
	if code, _, e := tab("add", "--url", "https://example.com/two", "--ws", "example-app-t11"); code != 0 {
		t.Fatalf("add --ws only: %s", e)
	}
	if tabs, _ := st.ListTabs(context.Background(), p.ID, "example-app-t11"); len(tabs) != 2 {
		t.Errorf("workstream tabs = %+v", tabs)
	}
}

func TestTab_LsRmFocusMove(t *testing.T) {
	st := tabEnv(t, "example-app-t11")
	for _, u := range []string{"https://a.example.com/x", "https://b.example.com/y", "https://c.example.com/z"} {
		if code, _, e := tab("add", "--url", u); code != 0 {
			t.Fatalf("add %s: %s", u, e)
		}
	}
	code, out, _ := tab("ls", "--json")
	var rows []tabJSON
	if code != 0 || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 3 {
		t.Fatalf("ls --json: %d %q", code, out)
	}
	if !rows[0].Active || rows[1].Active {
		t.Errorf("first tab should be the implicit active one: %+v", rows)
	}
	if code, _, e := tab("focus", "2"); code != 0 {
		t.Fatalf("focus 2: %s", e)
	}
	_, out, _ = tab("ls", "--json")
	_ = json.Unmarshal([]byte(out), &rows)
	if !rows[1].Active {
		t.Errorf("focus 2 did not stick: %+v", rows)
	}
	// Flags after the positional still parse.
	if code, _, e := tab("move", "3", "1", "--ws", "example-app-t11"); code != 0 {
		t.Fatalf("move: %s", e)
	}
	_, out, _ = tab("ls")
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 3 || !strings.Contains(lines[0], "c.example.com") {
		t.Errorf("after move:\n%s", out)
	}
	if code, _, e := tab("rm", "https://a.example.com/x"); code != 0 {
		t.Fatalf("rm by url: %s", e)
	}
	if code, _, e := tab("rm", "9"); code == 0 || !strings.Contains(e, "no tab") {
		t.Errorf("rm 9 should fail: %d %q", code, e)
	}
	p, _ := st.GetProjectByName(context.Background(), "example-app")
	if tabs, _ := st.ListTabs(context.Background(), p.ID, "example-app-t11"); len(tabs) != 2 {
		t.Errorf("tabs after rm = %+v", tabs)
	}
}

func TestTab_Errors(t *testing.T) {
	tabEnv(t, "")
	for _, args := range [][]string{
		{},
		{"add"},
		{"add", "--url", "u", "--cmd", "c"},
		{"add", "--url", "u", "--refresh", "soon"},
		{"add", "--bogus"},
		{"add", "--url", "u", "--project", "example-app"},           // project without ws
		{"add", "--cmd", "cmdr internal-needs-you --bead b --show"}, // plumbing, not a tab
		{"frobnicate"},
	} {
		if code, _, _ := tab(args...); code == 0 {
			t.Errorf("cmdr tab %v should fail", args)
		}
	}
}
