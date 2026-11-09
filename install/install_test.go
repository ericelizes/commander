package install

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestLaunchdPlistsCapProcesses(t *testing.T) {
	for file, limit := range map[string]string{
		"launchd/org.commander.plist":      "3000",
		"launchd/org.commander-tmux.plist": "3000",
	} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		s := string(raw)
		for _, key := range []string{"HardResourceLimits", "SoftResourceLimits"} {
			if !strings.Contains(s, "<key>"+key+"</key>") {
				t.Errorf("%s: missing %s", file, key)
			}
		}
		if n := strings.Count(s, "<key>NumberOfProcesses</key>\n        <integer>"+limit+"</integer>"); n != 2 {
			t.Errorf("%s: want NumberOfProcesses %s in both limit dicts, found %d", file, limit, n)
		}
		if path, err := exec.LookPath("plutil"); err == nil {
			if out, err := exec.Command(path, "-lint", file).CombinedOutput(); err != nil {
				t.Errorf("%s: plutil: %s", file, out)
			}
		}
	}
}

func TestSystemdUnitsCapProcesses(t *testing.T) {
	for file, limit := range map[string]string{
		"systemd/commander.service":      "3000",
		"systemd/commander-tmux.service": "3000",
	} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "TasksMax="+limit) {
			t.Errorf("%s: missing TasksMax=%s", file, limit)
		}
		// RLIMIT_NPROC is per-UID on Linux: a desktop session's threads alone
		// exceed any cap sized for the engine, and cmdrd then cannot fork.
		if strings.Contains(string(raw), "\nLimitNPROC=") {
			t.Errorf("%s: sets LimitNPROC (per-UID on Linux; starves cmdrd on a desktop)", file)
		}
	}
}

// The keeper must start under launchd/systemd without a terminal: a bare
// `tmux -D` exits "open terminal failed: not a terminal" and crash-looped
// ~4500 times, leaving only the legacy job to keep the ops server alive.
func TestTmuxKeeperUnitsHaveASession(t *testing.T) {
	for file, wants := range map[string][]string{
		"launchd/org.commander-tmux.plist": {"new-session -d -s keeper", "exit-empty off", "-L commander-ops"},
		"systemd/commander-tmux.service":   {"new-session -d -s keeper", "exit-empty off", "-L commander-ops"},
	} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		s := string(raw)
		for _, want := range wants {
			if !strings.Contains(s, want) {
				t.Errorf("%s: missing %q", file, want)
			}
		}
		if strings.Contains(s, "commander-ops -D") {
			t.Errorf("%s: still runs tmux -D, which needs a terminal", file)
		}
	}
}
