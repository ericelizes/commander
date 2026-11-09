package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/managerguard"
)

func hookIn(cmd string) string {
	return `{"tool_name":"Bash","cwd":"/repo","tool_input":{"command":` + jsonString(cmd) + `}}`
}

func jsonString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func TestOperatorGuardRefusesDirectBeadsWriteAndLogsIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_AGENT_WRITE_GUARD", "")
	var out bytes.Buffer
	runInternalOperatorGuard([]string{"--role", "operator-op-x"}, strings.NewReader(hookIn("bd --db /r/.beads/beads.db comments add x-1 done")), &out)
	if !strings.Contains(out.String(), "cmdr internal-comment") {
		t.Fatalf("no refusal naming the cmdr verb: %q", out.String())
	}
	raw, _ := os.ReadFile(filepath.Join(home, "logs", "agent-write-refusals.jsonl"))
	if !strings.Contains(string(raw), "operator-op-x") || !strings.Contains(string(raw), `"verb":"comments"`) {
		t.Fatalf("not logged: %q", raw)
	}
	if _, err := os.Stat(filepath.Join(home, "logs", "operator-refusals.jsonl")); err == nil {
		t.Error("beads write also went to the build-slot log")
	}
	// reads, the kill switch, and a non-agent role are untouched
	for _, tc := range []struct{ role, env, cmd string }{
		{"operator-op-x", "", "bd --db /r/.beads/beads.db show x-1"},
		{"operator-op-x", "", "bd comments x-1"},
		{"operator-op-x", "off", "bd close x-1"},
		{"commander", "", "bd close x-1"},
		{"", "", "bd close x-1"},
	} {
		t.Setenv("COMMANDER_AGENT_WRITE_GUARD", tc.env)
		var o bytes.Buffer
		runInternalOperatorGuard([]string{"--role", tc.role}, strings.NewReader(hookIn(tc.cmd)), &o)
		if o.Len() != 0 {
			t.Errorf("%+v: refused: %s", tc, o.String())
		}
	}
}

func TestManagerGuardRefusesDirectBeadsWriteWithoutMail(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_AGENT_WRITE_GUARD", "")
	var out bytes.Buffer
	runInternalManagerGuard([]string{"--role", "manager-x"}, strings.NewReader(hookIn("bd dep add a b")), &out)
	if !strings.Contains(out.String(), "cmdr internal-dep") {
		t.Fatalf("no refusal naming the cmdr verb: %q", out.String())
	}
	raw, _ := os.ReadFile(filepath.Join(home, "logs", "agent-write-refusals.jsonl"))
	if !strings.Contains(string(raw), "manager-x") {
		t.Fatalf("not logged: %q", raw)
	}
	if _, err := os.Stat(filepath.Join(home, "logs", "manager-refusals.jsonl")); err == nil {
		t.Error("beads write logged as a Manager doing an Operator's work")
	}
	for _, cmd := range []string{"bd show x-1", "bd dep list x-1", "bd label list x-1", "cmdr internal-comment --project p --bead b --text hi"} {
		var o bytes.Buffer
		runInternalManagerGuard([]string{"--role", "manager-x"}, strings.NewReader(hookIn(cmd)), &o)
		if o.Len() != 0 {
			t.Errorf("%q refused: %s", cmd, o.String())
		}
	}
	t.Setenv("COMMANDER_AGENT_WRITE_GUARD", "off")
	var o bytes.Buffer
	runInternalManagerGuard([]string{"--role", "manager-x"}, strings.NewReader(hookIn("bd close x-1")), &o)
	if o.Len() != 0 {
		t.Errorf("kill switch ignored: %s", o.String())
	}
}

func TestInternalCommentAndDepGoThroughTheClient(t *testing.T) {
	mock := &beads.MockClient{}
	setupObjectiveProject(t, mock)
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalComment([]string{"--project", "manifest", "--bead", "x-1", "--text", "final report"}, strings.NewReader(""), stdout, stderr); code != 0 {
		out, e := read()
		t.Fatalf("comment exit=%d %s %s", code, out, e)
	}
	stdout, stderr, read = withTempOutputFiles(t)
	if code := runInternalComment([]string{"--project", "manifest", "--bead", "x-1", "--file", "-"}, strings.NewReader("from stdin"), stdout, stderr); code != 0 {
		out, e := read()
		t.Fatalf("comment stdin exit=%d %s %s", code, out, e)
	}
	stdout, stderr, read = withTempOutputFiles(t)
	if code := runInternalComment([]string{"--project", "manifest", "--bead", "x-1", "--text", "  "}, strings.NewReader(""), stdout, stderr); code == 0 {
		t.Error("empty comment accepted")
	}
	stdout, stderr, read = withTempOutputFiles(t)
	if code := runInternalDep([]string{"--project", "manifest", "add", "x-2", "x-1"}, stdout, stderr); code != 0 {
		out, e := read()
		t.Fatalf("dep exit=%d %s %s", code, out, e)
	}
	stdout, stderr, read = withTempOutputFiles(t)
	if code := runInternalPhase([]string{"--project", "manifest", "--bead", "x-1", "--phase", "planning", "--reason", "done brainstorming"}, stdout, stderr); code != 0 {
		out, e := read()
		t.Fatalf("phase exit=%d %s %s", code, out, e)
	}
	stdout, stderr, _ = withTempOutputFiles(t)
	if code := runInternalDep([]string{"--project", "manifest", "drop", "x-2", "x-1"}, stdout, stderr); code == 0 {
		t.Error("unknown dep action accepted")
	}
	calls := strings.Join(mock.Calls, "\n")
	for _, want := range []string{"AddComment", "final report", "from stdin", "DependOn", "SetState"} {
		if !strings.Contains(calls, want) {
			t.Errorf("calls lack %q:\n%s", want, calls)
		}
	}
}

func TestCreateObjectiveOperationalLabel(t *testing.T) {
	mock := &beads.MockClient{CreateID: "x-1.9"}
	setupObjectiveProject(t, mock)
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "sub task", "--parent", "x-1", "--operational"}, stdout, stderr); code != 0 {
		out, e := read()
		t.Fatalf("exit=%d %s %s", code, out, e)
	}
	if c := mock.Calls[0]; !strings.Contains(c, "kind:operational") || strings.Contains(c, "kind:objective") {
		t.Fatalf("labels: %s", c)
	}
	stdout, stderr, _ = withTempOutputFiles(t)
	if code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "t", "--operational"}, stdout, stderr); code == 0 {
		t.Error("--operational without --parent accepted")
	}
}

// cmdrVerbsInMain is every `case "internal-x"` / top-level verb main dispatches.
func cmdrVerbsInMain(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	verbs := map[string]bool{}
	for _, m := range regexp.MustCompile(`"([a-z][a-z-]*)"`).FindAllStringSubmatch(string(raw), -1) {
		verbs[m[1]] = true
	}
	return verbs
}

func TestEveryRefusedVerbNamesAnExistingCmdrVerb(t *testing.T) {
	verbs := cmdrVerbsInMain(t)
	named := regexp.MustCompile(`cmdr (internal-[a-z-]+)`)
	for _, v := range managerguard.RefusedVerbs() {
		reason := managerguard.BeadsWriteReason(v)
		ms := named.FindAllStringSubmatch(reason, -1)
		if len(ms) == 0 {
			t.Errorf("verb %q: reason names no cmdr verb: %s", v, reason)
		}
		for _, m := range ms {
			if !verbs[m[1]] {
				t.Errorf("verb %q: reason names cmdr %s, which main.go does not dispatch", v, m[1])
			}
		}
		if strings.Contains(reason, "the matching cmdr verb") {
			t.Errorf("verb %q falls back to the generic hint", v)
		}
	}
}

func runVerb(t *testing.T, f func(*os.File, *os.File) int) (int, string) {
	t.Helper()
	stdout, stderr, read := withTempOutputFiles(t)
	code := f(stdout, stderr)
	out, e := read()
	return code, out + e
}

func TestInternalDepRmLabelUpdateAndCloseReason(t *testing.T) {
	mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "x-2", Status: "open"}}
	setupObjectiveProject(t, mock)
	must := func(name string, f func(*os.File, *os.File) int) {
		t.Helper()
		if code, out := runVerb(t, f); code != 0 {
			t.Fatalf("%s exit=%d %s", name, code, out)
		}
	}
	must("dep rm", func(o, e *os.File) int {
		return runInternalDep([]string{"--project", "manifest", "rm", "x-2", "x-1"}, o, e)
	})
	must("label add", func(o, e *os.File) int {
		return runInternalLabel([]string{"--project", "manifest", "--bead", "x-2", "add", "regression"}, o, e)
	})
	must("update", func(o, e *os.File) int {
		return runInternalUpdate([]string{"--project", "manifest", "--bead", "x-2", "--description", "d", "--priority", "1", "--notes", "n"}, strings.NewReader(""), o, e)
	})
	must("close", func(o, e *os.File) int {
		return runInternalCloseObjective([]string{"--project", "manifest", "--bead", "x-2", "--reason", "superseded by x-9"}, o, e)
	})
	calls := strings.Join(mock.Calls, "\n")
	for _, want := range []string{"RemoveDep:x-2->x-1", "SetLabel:x-2|label=regression|on=true", "UpdateFields:x-2|--description d --notes n --priority 1", "CloseWithReason:x-2|reason=superseded by x-9"} {
		if !strings.Contains(calls, want) {
			t.Errorf("calls lack %q:\n%s", want, calls)
		}
	}
}

func TestInternalLabelRefusesStateLabelsNamingTheVerb(t *testing.T) {
	mock := &beads.MockClient{}
	setupObjectiveProject(t, mock)
	for label, verb := range map[string]string{
		"waiting:1:user": "--waiting", "kind:track": "--workstream", "kind:workstream": "--workstream", "hold": "--hold", "viz:2": "--viz",
		"gate": "internal-gate", "decision:answered": "--decision", "needs-user": "--decision", "cmdr:planning": "internal-phase",
		"parked-until:5": "--park", "proposed:gate:1": "proposals",
	} {
		code, out := runVerb(t, func(o, e *os.File) int {
			return runInternalLabel([]string{"--project", "manifest", "--bead", "x-2", "add", label}, o, e)
		})
		if code == 0 || !strings.Contains(out, verb) {
			t.Errorf("label %q: exit=%d out=%s", label, code, out)
		}
	}
	for _, c := range mock.Calls {
		if strings.HasPrefix(c, "SetLabel") {
			t.Errorf("a refused label was written: %s", c)
		}
	}
}

func TestInternalUpdateRefusesBadInput(t *testing.T) {
	setupObjectiveProject(t, &beads.MockClient{})
	for _, args := range [][]string{
		{"--project", "manifest", "--bead", "x-2"},
		{"--project", "manifest", "--bead", "x-2", "--priority", "9"},
		{"--project", "manifest", "--bead", "x-2", "--title", " "},
	} {
		if code, _ := runVerb(t, func(o, e *os.File) int { return runInternalUpdate(args, strings.NewReader(""), o, e) }); code == 0 {
			t.Errorf("accepted %v", args)
		}
	}
}

func TestCreateObjectiveDepsKeepsTheDirection(t *testing.T) {
	mock := &beads.MockClient{CreateID: "x-1.9"}
	setupObjectiveProject(t, mock)
	code, out := runVerb(t, func(o, e *os.File) int {
		return runInternalCreateObjective([]string{"--project", "manifest", "--title", "build", "--parent", "x-1", "--deps", "blocked-by:x-1.1,x-1.2,blocks:x-1.3"}, o, e)
	})
	if code != 0 {
		t.Fatalf("exit=%d %s", code, out)
	}
	calls := strings.Join(mock.Calls, "\n")
	for _, want := range []string{"DependOn:x-1.9->x-1.1", "DependOn:x-1.9->x-1.2", "DependOn:x-1.3->x-1.9"} {
		if !strings.Contains(calls, want) {
			t.Errorf("calls lack %q:\n%s", want, calls)
		}
	}
	if code, _ := runVerb(t, func(o, e *os.File) int {
		return runInternalCreateObjective([]string{"--project", "manifest", "--title", "t", "--parent", "x-1", "--deps", "after:x-1.1"}, o, e)
	}); code == 0 {
		t.Error("unknown dependency kind accepted")
	}
}

func TestGuardsRefuseEvalOfBeadsWrite(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv("COMMANDER_AGENT_WRITE_GUARD", "")
	run := func(role, cmd string) string {
		var o bytes.Buffer
		if strings.HasPrefix(role, "manager-") {
			runInternalManagerGuard([]string{"--role", role}, strings.NewReader(hookIn(cmd)), &o)
		} else {
			runInternalOperatorGuard([]string{"--role", role}, strings.NewReader(hookIn(cmd)), &o)
		}
		return o.String()
	}
	for _, role := range []string{"manager-x", "operator-op-x"} {
		for _, cmd := range []string{`eval "bd close X"`, `eval bd close X`, `eval 'bd close X'`, `env A=1 eval "bd close X"`, `cd /r && eval "bd close X"`} {
			if !strings.Contains(run(role, cmd), "cmdr internal-close-objective") {
				t.Errorf("%s: %q not refused", role, cmd)
			}
		}
		if out := run(role, `eval "bd show X"`); out != "" {
			t.Errorf("%s: eval of a read refused: %s", role, out)
		}
	}
	t.Setenv("COMMANDER_AGENT_WRITE_GUARD", "off")
	for _, role := range []string{"manager-x", "operator-op-x"} {
		if out := run(role, `eval "bd close X"`); out != "" {
			t.Errorf("%s: kill switch ignored: %s", role, out)
		}
	}
}
