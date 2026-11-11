package execution

import (
	"github.com/example-user/commander/internal/testguard"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	cleanup := testguard.Isolate()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
