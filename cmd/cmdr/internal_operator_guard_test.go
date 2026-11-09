package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOperatorGuardRefusesBareSuiteAndLogs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	var out bytes.Buffer
	in := `{"tool_name":"Bash","tool_input":{"command":"go test ./..."}}`
	if code := runInternalOperatorGuard([]string{"--role", "operator-op-x"}, strings.NewReader(in), &out); code != 0 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(out.String(), "internal-slot run") {
		t.Fatalf("no refusal: %q", out.String())
	}
	raw, _ := os.ReadFile(filepath.Join(home, "logs", "operator-refusals.jsonl"))
	if !strings.Contains(string(raw), "operator-op-x") {
		t.Fatalf("not logged: %q", raw)
	}
}

func TestOperatorGuardAllowsWrappedTargetedAndFailsOpen(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	for _, in := range []string{
		`{"tool_name":"Bash","tool_input":{"command":"cmdr internal-slot run -- go test ./..."}}`,
		`{"tool_name":"Bash","tool_input":{"command":"go test -p 2 ./internal/slot -run X"}}`,
		`not json`,
		``,
	} {
		var out bytes.Buffer
		if code := runInternalOperatorGuard([]string{"--role", "operator-op-x"}, strings.NewReader(in), &out); code != 0 || out.Len() != 0 {
			t.Errorf("%q: code=%d out=%q", in, code, out.String())
		}
	}
	var out bytes.Buffer
	runInternalOperatorGuard([]string{"--role", "manager-x"}, strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"go test ./..."}}`), &out)
	if out.Len() != 0 {
		t.Error("guard must only act on operator roles")
	}
}
