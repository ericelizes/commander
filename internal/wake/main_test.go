package wake

import (
	"os"
	"testing"

	"github.com/example-user/commander/internal/testguard"
)

// TestMain keeps tests on a private default tmux server.
func TestMain(m *testing.M) {
	cleanup := testguard.IsolateTmux()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
