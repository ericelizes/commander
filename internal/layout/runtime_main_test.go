package layout

import (
	"github.com/example-user/commander/internal/testguard"
	"os"
	"testing"

	"github.com/example-user/commander/internal/agentrt"
)

// TestMain pins the runtime so a developer's ~/.commander/config.toml can't
// flip these opencode-shaped tests; Claude tests opt in with t.Setenv.
func TestMain(m *testing.M) {
	os.Setenv(agentrt.EnvVar, string(agentrt.Opencode))
	cleanup := testguard.Isolate()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
