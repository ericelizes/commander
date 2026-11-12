package workflowtemplate

import (
	"os"
	"testing"
)

// testdata/pr-review-v1.json is pr-review version 1, the six-step block
// snapshotted into blocks plugged before the two-step review. They still parse
// and finish; tests in other packages read the same file.
func legacyTemplate(t *testing.T) *Template {
	t.Helper()
	raw, err := os.ReadFile("testdata/pr-review-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return tpl
}

// The catalog fields are omitempty, so a version 1 snapshot keeps the hash its
// plugged blocks carry (workflow-hash:6747f7cebb3d in the fixtures).
func TestLegacySnapshotKeepsItsHash(t *testing.T) {
	if got := legacyTemplate(t).Hash(); got != "6747f7cebb3d" {
		t.Fatalf("version 1 hash = %s, plugged blocks carry 6747f7cebb3d", got)
	}
}
