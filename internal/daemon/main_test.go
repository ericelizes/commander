package daemon

import (
	"os"
	"testing"

	"github.com/example-user/commander/internal/testguard"
)

func TestMain(m *testing.M) {
	cleanup := testguard.Isolate()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
