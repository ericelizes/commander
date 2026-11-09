package main

import (
	"strings"
	"testing"
)

func TestPushLabelsReserved(t *testing.T) {
	for _, label := range []string{"pushed:3", "pushed-first:123", "pushed-last:456", "pushed-flagged"} {
		verb, ok := reservedLabel(label)
		if !ok || !strings.Contains(verb, "--push-reset") {
			t.Errorf("%s: %q, %v", label, verb, ok)
		}
	}
	if _, ok := reservedLabel("pushed-forward"); ok {
		t.Fatal("ordinary label was reserved")
	}
}
