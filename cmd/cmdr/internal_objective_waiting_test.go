package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
)

func TestApplyWaitingReplacesTheSet(t *testing.T) {
	bc := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-1.2", Labels: []string{"owner:x", "waiting:5:user", "wait-nudged"}}}
	waits, err := parseWaitingFlags([]string{"ci:#4101", "review:reviewer-a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyWaiting(context.Background(), bc, "/p", "p-1.2", waits, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(bc.Calls, "\n")
	for _, want := range []string{
		"SetLabel:p-1.2|label=waiting:5:user|on=false",
		"SetLabel:p-1.2|label=wait-nudged|on=false",
		"SetLabel:p-1.2|label=waiting:100:ci:4101|on=true",
		"SetLabel:p-1.2|label=waiting:100:review:reviewer-a|on=true",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "label=owner:x") {
		t.Error("touched an unrelated label")
	}
}

func TestWaitingNoneClears(t *testing.T) {
	waits, err := parseWaitingFlags([]string{"none"})
	if err != nil || waits != nil {
		t.Fatalf("none = %v, %v", waits, err)
	}
	bc := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-1", Labels: []string{"waiting:5:user"}}}
	if err := applyWaiting(context.Background(), bc, "/p", "p-1", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(bc.Calls, "\n"); strings.Contains(got, "on=true") || !strings.Contains(got, "waiting:5:user|on=false") {
		t.Errorf("calls:\n%s", got)
	}
	if _, err := parseWaitingFlags([]string{"none", "user"}); err == nil {
		t.Error("none combined with another wait was accepted")
	}
}
