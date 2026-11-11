package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestExecutionAttributionCannotBorrowPlanningCommands(t *testing.T) {
	for _, key := range []string{"COMMANDER_RUN_ID", "COMMANDER_RUN_SESSION", "COMMANDER_MANAGER_ROLE"} {
		t.Run(key, func(t *testing.T) {
			for _, entry := range []string{"COMMANDER_ROLE", "COMMANDER_RUN_ID", "COMMANDER_RUN_SESSION", "COMMANDER_MANAGER_ROLE"} {
				t.Setenv(entry, "")
			}
			t.Setenv("COMMANDER_ROLE", "commander")
			t.Setenv(key, "execution-context")
			commands := []struct {
				name string
				run  func(*bytes.Buffer, *bytes.Buffer) int
			}{
				{"question", func(out, err *bytes.Buffer) int {
					return runQuestion([]string{"answer", "--id", "question", "--text", "Agree"}, out, err)
				}},
				{"jurisdiction", func(out, err *bytes.Buffer) int {
					return runJurisdiction([]string{"transfer", "--project-id", "1", "--bead", "work"}, out, err)
				}},
				{"ready", func(out, err *bytes.Buffer) int {
					return runExecution([]string{"ready", "--project-id", "1", "--bead", "work"}, out, err)
				}},
				{"orchestration", func(out, err *bytes.Buffer) int {
					return runOrchestration([]string{"set", "--revision", "1", "--mode", "active", "--reason", "test"}, out, err)
				}},
			}
			for _, command := range commands {
				var out, errOut bytes.Buffer
				if code := command.run(&out, &errOut); code == 0 || !strings.Contains(errOut.String(), "execution sessions") {
					t.Fatal(command.name, code, errOut.String())
				}
			}
			// Human decisions use file writers; this must refuse before resolving any
			// project, opening state or saving a journal.
			out, errFile, e := tempExecutionFiles(t)
			if e != nil {
				t.Fatal(e)
			}
			if code := runInternalResolveNeeds([]string{"--project", "unknown", "--bead", "work", "--action", "approve"}, out, errFile); code == 0 {
				t.Fatal("execution recorded human direction")
			}
			raw, e := os.ReadFile(errFile.Name())
			if e != nil || !strings.Contains(string(raw), "execution sessions") {
				t.Fatal(string(raw), e)
			}
		})
	}
}
func tempExecutionFiles(t *testing.T) (*os.File, *os.File, error) {
	t.Helper()
	out, e := os.CreateTemp(t.TempDir(), "stdout")
	if e != nil {
		return nil, nil, e
	}
	errFile, e := os.CreateTemp(t.TempDir(), "stderr")
	if e != nil {
		out.Close()
		return nil, nil, e
	}
	t.Cleanup(func() { out.Close(); errFile.Close() })
	return out, errFile, nil
}
