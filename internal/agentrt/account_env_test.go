package agentrt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// claudeHomes puts HOME and COMMANDER_HOME in temp dirs with an inherited
// CLAUDE_CONFIG_DIR, as a pane has under a tmux server started from one.
func claudeHomes(t *testing.T) (user, home, inherited string) {
	t.Helper()
	user, home, inherited = t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", inherited)
	return user, home, inherited
}

func writeTranscript(t *testing.T, configDir, id string) {
	t.Helper()
	p := filepath.Join(configDir, "projects", "-work", id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

const paneSession = "0b6f1c1e-2a4d-4c3b-9e8f-1a2b3c4d5e6f"

func TestInteractiveDefaultClearsInheritedConfigDir(t *testing.T) {
	user, _, inherited := claudeHomes(t)
	got := ClaudeInteractiveCmd("", "commander", "", paneSession)
	if !strings.HasPrefix(got, "env -u CLAUDE_CONFIG_DIR DISABLE_AUTOUPDATER=1 claude ") {
		t.Errorf("default pane keeps the inherited profile: %s", got)
	}

	// Only the inherited dir has the session: the child will not see it.
	writeTranscript(t, inherited, paneSession)
	if got := ClaudeInteractiveCmd("", "commander", "", paneSession); !strings.HasSuffix(got, " --session-id "+paneSession) {
		t.Errorf("resume read the inherited dir: %s", got)
	}
	writeTranscript(t, filepath.Join(user, ".claude"), paneSession)
	if got := ClaudeInteractiveCmd("", "commander", "", paneSession); !strings.HasSuffix(got, " --resume "+paneSession) {
		t.Errorf("resume missed ~/.claude: %s", got)
	}
}

// Panes do not follow the role's .account: they run on default, as they did
// before accounts, and only Engine starts honour it.
func TestInteractiveIgnoresTheRoleAccount(t *testing.T) {
	user, home, _ := claudeHomes(t)
	dir := filepath.Join(home, "claude-accounts", "second")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "chat"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "chat", "manager-ws-1.account"), []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := ClaudeInteractiveCmd("", "manager-ws-1", "", paneSession)
	if !strings.HasPrefix(got, "env -u CLAUDE_CONFIG_DIR DISABLE_AUTOUPDATER=1 claude ") {
		t.Errorf("pane followed the role's account: %s", got)
	}
	writeTranscript(t, dir, paneSession)
	if got := ClaudeInteractiveCmd("", "manager-ws-1", "", paneSession); !strings.HasSuffix(got, " --session-id "+paneSession) {
		t.Errorf("resume read the account's dir: %s", got)
	}
	writeTranscript(t, filepath.Join(user, ".claude"), paneSession)
	if got := ClaudeInteractiveCmd("", "manager-ws-1", "", paneSession); !strings.HasSuffix(got, " --resume "+paneSession) {
		t.Errorf("resume missed ~/.claude: %s", got)
	}
}
