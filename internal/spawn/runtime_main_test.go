package spawn

import (
	"github.com/example-user/commander/internal/testguard"
	"os"
	"testing"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/chat/chattest"
)

// TestMain pins the runtime so a developer's ~/.commander/config.toml can't
// flip these opencode-shaped tests; Claude tests opt in with t.Setenv.
func TestMain(m *testing.M) {
	chat.MaybeRunHost()
	chat.MaybeRunCodexBridge()
	chattest.MaybeRunFakeCodex()
	chattest.MaybeRunFake()
	os.Setenv(agentrt.EnvVar, string(agentrt.Opencode))
	defaultAgentOrder = func() []string { return []string{chat.RuntimeClaude} } // no agent goes to Codex unless a test asks
	cleanup := testguard.Isolate()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
