package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/brief"
)

func TestBriefGuardHookRefusesAllButReadCommands(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(brief.RunDirEnv, dir)
	answer := `{"tool_name":"StructuredOutput","tool_input":{"markdown":"## Moving today"}}`
	var early bytes.Buffer
	runInternalBriefGuard(strings.NewReader(answer), &early)
	if !strings.Contains(early.String(), "investigate first") {
		t.Fatal("the answer waits for the checks:", early.String())
	}
	for _, c := range []struct {
		input string
		deny  bool
	}{
		{`{"tool_name":"Bash","tool_input":{"command":"gh pr view 12 -R acme/app"}}`, false},
		{`{"tool_name":"Bash","tool_input":{"command":"gh pr merge 12 -R acme/app"}}`, true},
		{`{"tool_name":"Bash","tool_input":{"command":"cmdr workstream-statuses; rm -rf ~"}}`, true},
		{`{"tool_name":"Write","tool_input":{"file_path":"/tmp/x","content":"x"}}`, true},
	} {
		var out bytes.Buffer
		if code := runInternalBriefGuard(strings.NewReader(c.input), &out); code != 0 {
			t.Fatal(code)
		}
		denied := strings.Contains(out.String(), `"permissionDecision":"deny"`)
		if denied != c.deny {
			t.Errorf("%s: denied=%v (%s)", c.input, denied, out.String())
		}
	}
	for i := 0; i < brief.MinToolCalls; i++ {
		runInternalBriefGuard(strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"cmdr workstream-statuses"}}`), &bytes.Buffer{})
	}
	var late bytes.Buffer
	runInternalBriefGuard(strings.NewReader(answer), &late)
	if late.Len() != 0 {
		t.Fatal("after the checks the answer is taken:", late.String())
	}
}

func TestBriefReadRefusesWritesBeforeConnecting(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runInternalBriefRead([]string{"sql", "DELETE FROM bead_tasks"}, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "only SELECT") {
		t.Fatal(code, errOut.String())
	}
	if code := runInternalBriefRead([]string{"rm", "x"}, &out, &errOut); code != 2 {
		t.Fatal(code)
	}
}
