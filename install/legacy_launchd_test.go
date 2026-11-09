package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyLaunchdUpgradePreservesKeeper(t *testing.T) {
	home := t.TempDir()
	agents := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agents, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"com.example.commander", "com.example.commander-tmux"} {
		if err := os.WriteFile(filepath.Join(agents, name+".plist"), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", "-c", `source ./legacy-launchd.sh
launchctl() { printf '%s\n' "$*" >> "$HOME/calls"; }
legacy_keeper_loaded
retire_legacy_keeper_plist
cleanup_legacy_daemon
`)
	cmd.Env = append(os.Environ(), "HOME="+home)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(home, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "bootout") && strings.Contains(line, "-tmux") {
			t.Fatalf("keeper stopped: %s", raw)
		}
	}
	if !strings.Contains(string(raw), "bootout") {
		t.Fatalf("daemon not cleaned up: %s", raw)
	}
	entries, err := os.ReadDir(agents)
	if err != nil || len(entries) != 0 {
		t.Fatalf("old login entries remain: %v %v", entries, err)
	}
}
