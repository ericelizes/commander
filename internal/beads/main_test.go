package beads

import (
	"os"
	"testing"

	"github.com/example-user/commander/internal/testguard"
)

func TestMain(m *testing.M) {
	os.Setenv("COMMANDER_BEADS_BIN", "br") // tests assert br argv; bd tests opt in
	cleanup := testguard.Isolate()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
