package operatorguard

import (
	"strings"
	"testing"
)

func TestOperatorGuardRefusesCockpitNamingHelp(t *testing.T) {
	for _, command := range []string{"cmdr 2>&1 | grep -i usage", "cmdr", "commander", "cmdr --project demo", "cmdr --task demo-1", "cmdr root", "cmdr restart", "env -u TMUX cmdr"} {
		v := Classify(command)
		if !v.Deny || !v.Cockpit {
			t.Errorf("Classify(%q) = %+v", command, v)
			continue
		}
		if out := string(HookOutputFor(v)); !strings.Contains(out, "cmdr --help") {
			t.Errorf("%q: hook output %s", command, out)
		}
	}
	for _, command := range []string{"command -v cmdr", "cmdr --help", "cmdr board", "cmdr internal-comment --project p --bead b --text x", "cmdr internal-slot run -- go test ./..."} {
		if v := Classify(command); v.Deny {
			t.Errorf("Classify(%q) refused: %+v", command, v)
		}
	}
}
