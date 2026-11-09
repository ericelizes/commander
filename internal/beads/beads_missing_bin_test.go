package beads

import (
	"context"
	"strings"
	"testing"
)

func TestMissingBinaryNamesEnv(t *testing.T) {
	_, _, err := runBr(context.Background(), t.TempDir(), "bd-does-not-exist-xyz", "list")
	if err == nil || !strings.Contains(err.Error(), "COMMANDER_BEADS_BIN") {
		t.Fatalf("want an error naming COMMANDER_BEADS_BIN, got %v", err)
	}
}
