package tui

import "testing"

func TestSelfExeRefusesInsideTests(t *testing.T) {
	if exe, err := selfExe(); err == nil {
		t.Fatalf("selfExe = %q, want a refusal under go test", exe)
	}
}
