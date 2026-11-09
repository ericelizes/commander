package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/layout"
	"github.com/example-user/commander/internal/managerguard"
	"github.com/example-user/commander/internal/store"
)

// cockpitRefusalRig makes any tmux call or target resolution visible.
func cockpitRefusalRig(t *testing.T, stdin, stdout bool) *layout.MockRunner {
	t.Helper()
	mr := &layout.MockRunner{}
	mr.SetRunFunc(func([]string) ([]byte, []byte, error) { return nil, nil, nil })
	origDirector, origTerm, origResolve, origTask := newDirectorFn, stdioIsTerminalFn, resolveProjectRecFn, resolveTaskTargetFn
	origUpdate, origRestart := runUpdateCommandFn, reloadRestartFn
	// reload runs the install script: never for real from a test.
	runUpdateCommandFn = func(context.Context, *os.File, *os.File, string, string) error {
		t.Error("a refused reload must not run the install script")
		return errors.New("ran")
	}
	reloadRestartFn = func(context.Context, *os.File, *os.File) error {
		t.Error("a refused reload must not restart")
		return errors.New("ran")
	}
	newDirectorFn = func() *layout.Director { return layout.NewDirectorWith(mr) }
	stdioIsTerminalFn = func() (bool, bool) { return stdin, stdout }
	resolveProjectRecFn = func(string) (*store.Project, error) {
		t.Error("a refused target must not be resolved")
		return nil, errors.New("resolved")
	}
	resolveTaskTargetFn = func(string) (resolvedOpenTarget, error) {
		t.Error("a refused target must not be resolved")
		return resolvedOpenTarget{}, errors.New("resolved")
	}
	t.Cleanup(func() {
		newDirectorFn, stdioIsTerminalFn, resolveProjectRecFn, resolveTaskTargetFn = origDirector, origTerm, origResolve, origTask
		runUpdateCommandFn, reloadRestartFn = origUpdate, origRestart
	})
	return mr
}

var cockpitOpeningArgs = [][]string{
	nil, {"-p", "demo"}, {"--project", "demo"}, {"--project=demo"}, {"-t", "demo-1"}, {"--task", "demo-1"},
	{"root"}, {"projects"}, {"restart"}, {"restart", "--hard"}, {"reload"},
}

func TestCockpitRefusedForAnAgentOrWithNoTerminal(t *testing.T) {
	for _, tc := range []struct {
		name          string
		stdin, stdout bool
		env           map[string]string
		wantInMessage string
	}{
		{"role", true, true, map[string]string{"COMMANDER_ROLE": "operator-demo-1"}, "COMMANDER_ROLE"},
		{"manager role only", true, true, map[string]string{"COMMANDER_MANAGER_ROLE": "manager-demo"}, "COMMANDER_MANAGER_ROLE"},
		{"no stdin terminal", false, true, nil, "terminal"},
		{"no stdout terminal", true, false, nil, "terminal"},
		{"neither", false, false, nil, "terminal"},
		{"the waiver does not cover a role", false, false, map[string]string{"COMMANDER_ROLE": "manager-demo", cockpitNoTerminalEnv: "1"}, "COMMANDER_ROLE"},
	} {
		for _, args := range cockpitOpeningArgs {
			t.Run(tc.name+" "+strings.Join(args, " "), func(t *testing.T) {
				for k, v := range tc.env {
					t.Setenv(k, v)
				}
				mr := cockpitRefusalRig(t, tc.stdin, tc.stdout)
				stdout, stderr, readOutput := withTempOutputFiles(t)
				code := run(args, stdout, stderr)
				_, errOut := readOutput()
				if code != 2 {
					t.Fatalf("exit=%d want 2; stderr=%s", code, errOut)
				}
				if !strings.Contains(errOut, "cmdr --help") || !strings.Contains(errOut, tc.wantInMessage) {
					t.Fatalf("stderr=%q wants cmdr --help and %q", errOut, tc.wantInMessage)
				}
				if calls := mr.CallStrings(); len(calls) != 0 {
					t.Fatalf("a refusal must make no tmux call, got %v", calls)
				}
			})
		}
	}
}

func TestCockpitOpensInATerminalAndWithTheWaiver(t *testing.T) {
	for _, tc := range []struct {
		name          string
		stdin, stdout bool
		waiver        string
	}{
		{"terminal", true, true, ""},
		{"no terminal with the waiver", false, false, "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("COMMANDER_HOME", t.TempDir())
			t.Setenv(cockpitNoTerminalEnv, tc.waiver)
			mr := cockpitRefusalRig(t, tc.stdin, tc.stdout)
			stdout, stderr, _ := withTempOutputFiles(t)
			run(nil, stdout, stderr)
			if len(mr.CallStrings()) == 0 {
				t.Fatal("expected the launch to reach tmux")
			}
		})
	}
}

// Nothing that reads or asks for help is touched: an agent still gets its answer.
func TestCockpitRefusalLeavesOtherVerbsAlone(t *testing.T) {
	t.Setenv("COMMANDER_ROLE", "operator-demo-1")
	cockpitRefusalRig(t, false, false)
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}, {"--version"}, {"version"}} {
		stdout, stderr, readOutput := withTempOutputFiles(t)
		code := run(args, stdout, stderr)
		out, errOut := readOutput()
		if code != 0 || out == "" {
			t.Fatalf("%v: exit=%d stdout=%q stderr=%q", args, code, out, errOut)
		}
	}
	// A typo still reads as a typo.
	stdout, stderr, readOutput := withTempOutputFiles(t)
	if code := run([]string{"--project", "a", "--task", "b"}, stdout, stderr); code != 2 {
		t.Fatalf("exit=%d", code)
	}
	if _, errOut := readOutput(); !strings.Contains(errOut, "mutually exclusive") {
		t.Fatalf("stderr=%q", errOut)
	}
}

func TestManagerGuardRefusesCockpitWithoutMail(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	var out bytes.Buffer
	runInternalManagerGuard([]string{"--role", "manager-x"}, strings.NewReader(hookIn("cmdr 2>&1 | grep -i usage")), &out)
	if !strings.Contains(out.String(), "cmdr --help") {
		t.Fatalf("no refusal naming cmdr --help: %q", out.String())
	}
	if !strings.Contains(out.String(), "cockpit") {
		t.Fatalf("not the cockpit refusal: %q", out.String())
	}
	if raw, _ := os.ReadFile(filepath.Join(home, "logs", "manager-refusals.jsonl")); !strings.Contains(string(raw), "opens the cockpit") {
		t.Fatalf("not logged: %q", raw)
	}
	if shouldMail := managerguard.ShouldMail(home, "manager-x", time.Now()); !shouldMail {
		t.Fatal("a cockpit refusal took the Commander mail's dedupe slot")
	}
}
