package main

import (
	"github.com/example-user/commander/internal/beads"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStateWriteObjectiveBatchesAllFlags(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "br")
	log := filepath.Join(dir, "calls")
	t.Setenv("STATE_WRITE_CALLS", log)
	t.Setenv("COMMANDER_BEADS_BIN", script)
	body := `#!/bin/sh
printf '%s\n' "$*" >> "$STATE_WRITE_CALLS"
case "$1" in
show) if [ "$3" != "--json" ]; then printf 'ID: p-1\nLabels: kind:objective, owner:user, viz:1, waiting:1:ci:7\n'; exit; fi ;;
esac
case "$1" in
list|show) echo '[{"id":"p-1","status":"open","labels":["kind:objective","owner:user","viz:1","waiting:1:ci:#7"]}]' ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	setupObjectiveProject(t, beads.New())
	registerRunningOperator(t, "operator-x")
	stdout, stderr, read := withTempOutputFiles(t)
	code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "p-1", "--state", "in-progress", "--writes-workflow", "--owner", "operator-x", "--star", "true", "--viz", "2", "--waiting", "ci:#8"}, stdout, stderr)
	_, errOut := read()
	if code != 0 {
		t.Fatalf("code=%d: %s", code, errOut)
	}
	calls, _ := os.ReadFile(log)
	updates := 0
	for _, line := range strings.Split(string(calls), "\n") {
		if strings.HasPrefix(line, "update ") {
			updates++
			for _, want := range []string{"--status in_progress", "--add-label star", "--add-label owner:operator-x", "--remove-label owner:user", "--add-label viz:2", "--remove-label viz:1", ":ci:8"} {
				if !strings.Contains(line, want) {
					t.Errorf("missing %s: %s", want, line)
				}
			}
		}
	}
	if updates != 1 {
		t.Fatalf("updates=%d: %s", updates, calls)
	}
}

func TestStateWriteFailedRequirementWritesNothing(t *testing.T) {
	mock := &beads.MockClient{}
	setupObjectiveProject(t, mock)
	stdout, stderr, _ := withTempOutputFiles(t)
	code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "p-1", "--star", "true", "--state", "in-progress", "--writes-workflow"}, stdout, stderr)
	if code == 0 {
		t.Fatal("missing owner accepted")
	}
	for _, call := range mock.Calls {
		if strings.HasPrefix(call, "Set") {
			t.Fatalf("partial write: %s", call)
		}
	}
}
