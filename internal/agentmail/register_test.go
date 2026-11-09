package agentmail

import (
	"context"
	"strings"
	"testing"
)

// TestRegister_ProgramFlag is the U1/D7 regression guard: commander MUST pass
// `--program opencode-ai`, never `--program claude-code`.
func TestRegister_ProgramFlag(t *testing.T) {
	var capturedArgs []string
	captured := &cmdClient{
		exec: func(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
			capturedArgs = args
			return nil, nil, nil
		},
	}

	if err := captured.Register(context.Background(), "commander", "opencode-ai", "sonnet", "commander"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if !containsPair(capturedArgs, "--program", "opencode-ai") {
		t.Fatalf("expected --program opencode-ai in argv, got %v", capturedArgs)
	}
	for i, a := range capturedArgs {
		if a == "--program" && i+1 < len(capturedArgs) && capturedArgs[i+1] == "claude-code" {
			t.Fatalf("D7 regression: --program is claude-code, expected opencode-ai")
		}
	}
}

func TestRegister_FullArgv(t *testing.T) {
	var binName string
	var capturedArgs []string
	captured := &cmdClient{
		exec: func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
			binName = name
			capturedArgs = args
			return nil, nil, nil
		},
	}

	err := captured.Register(context.Background(), "myproj", "opencode-ai", "sonnet", "lead-myproj")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if binName != "am" {
		t.Errorf("binary = %q, want %q", binName, "am")
	}

	want := []string{
		"agents", "register",
		"--project", "myproj",
		"--program", "opencode-ai",
		"--model", "sonnet",
		"--name", "lead-myproj",
		"--json",
	}
	if strings.Join(capturedArgs, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v, want %v", capturedArgs, want)
	}
}

func TestRegister_Validation(t *testing.T) {
	c := &cmdClient{exec: func(context.Context, string, ...string) ([]byte, []byte, error) {
		return nil, nil, nil
	}}
	cases := []struct {
		name, project, program, model, agent string
	}{
		{"empty project", "", "opencode-ai", "sonnet", "x"},
		{"empty program", "p", "", "sonnet", "x"},
		{"empty model", "p", "opencode-ai", "", "x"},
		{"empty name", "p", "opencode-ai", "sonnet", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := c.Register(context.Background(), tc.project, tc.program, tc.model, tc.agent); err == nil {
				t.Errorf("expected validation error, got nil")
			}
		})
	}
}

// containsPair returns true iff args contains flag immediately followed by value.
func containsPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
