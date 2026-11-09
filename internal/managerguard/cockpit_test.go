package managerguard

import (
	"strings"
	"testing"
)

func TestOpensCockpit(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    bool
	}{
		{"cmdr", true},
		{"cmdr 2>&1 | grep -i usage", true},
		{"cmdr > /tmp/x", true},
		{"commander", true},
		{"/usr/local/bin/cmdr", true},
		{"bin/staged/cmdr", true},
		{"env -u TMUX cmdr", true},
		{"timeout 5 cmdr", true},
		{"(cmdr)", true},
		{"true && cmdr", true},
		{"echo $(cmdr)", true},
		{`bash -c "cmdr 2>&1"`, true},
		{`eval "cmdr"`, true},
		{"echo x | xargs cmdr", true},
		{"cmdr -p demo", true},
		{"cmdr --project demo", true},
		{"cmdr --project=demo", true},
		{"cmdr -t demo-1", true},
		{"cmdr --task demo-1", true},
		{"cmdr root", true},
		{"cmdr projects", true},
		{"cmdr restart", true},
		{"cmdr restart --hard", true},
		{"cmdr reload", true},

		{"command cmdr", true},
		{"command -p cmdr", true},
		{"command -v cmdr && cmdr", true},

		{"command -v cmdr", false},
		{"command -V cmdr", false},
		{"command -v cmdr >/dev/null 2>&1 && echo yes", false},
		{"if command -v cmdr; then echo yes; fi", false},
		{"! command -v commander", false},
		{"cmdr --help", false},
		{"cmdr -h", false},
		{"cmdr help", false},
		{"cmdr --version", false},
		{"cmdr restart --help", false},
		{"cmdr board", false},
		{"cmdr internal-comment --project p --bead b --text cmdr", false},
		{"cmdr tab ls", false},
		{"cmdr nonsense", false},
		{"which cmdr", false},
		{"echo cmdr", false},
		{"git grep cmdr", false},
		{"go build -o bin/cmdr ./cmd/cmdr", false},
		{"cmdr-test", false},
		{"ls cmd/cmdr", false},
	} {
		got := OpensCockpit(tc.command) != ""
		if got != tc.want {
			t.Errorf("OpensCockpit(%q) = %v, want %v", tc.command, got, tc.want)
		}
	}
}

func TestManagerGuardRefusesCockpitNamingHelp(t *testing.T) {
	// A worktree cwd must not turn this into the generic Manager refusal, and
	// "cmdr -p board" reads like a pure read to the read list.
	for _, tc := range []struct{ command, cwd string }{
		{"cmdr 2>&1 | grep usage", "/tmp"},
		{"cmdr", "/repo/.worktrees/op-x"},
		{"cmdr -p board", "/tmp"},
		{"cmdr restart", "/tmp"},
	} {
		v := Classify(tc.command, tc.cwd)
		if !v.Deny || !strings.HasPrefix(v.Why, CockpitWhy) {
			t.Errorf("Classify(%q, %q) = %+v", tc.command, tc.cwd, v)
		}
	}
	if !strings.Contains(string(CockpitHookOutput()), "cmdr --help") {
		t.Fatalf("hook output: %s", CockpitHookOutput())
	}
	for _, command := range []string{"command -v cmdr", "cmdr --help", "cmdr board"} {
		if v := Classify(command, "/tmp"); v.Deny {
			t.Errorf("Classify(%q) refused: %+v", command, v)
		}
	}
}
