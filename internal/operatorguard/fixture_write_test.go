package operatorguard

import (
	"os"
	"testing"
)

// The retained command is data only. Never execute this regression fixture.
func TestFixtureWriteIsNotInstallation(t *testing.T) {
	b, err := os.ReadFile("testdata/fixture-write-command.txt")
	if err != nil {
		t.Fatal(err)
	}
	if v := Classify(string(b)); v.Deny {
		t.Fatalf("source fixture write refused: %+v", v)
	}
}
