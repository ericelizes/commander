package main

import (
	"context"
	"github.com/example-user/commander/internal/beads"
	"os"
	"strings"
	"testing"
)

func TestFiledMinorGateAdvice(t *testing.T) {
	for _, kind := range []string{beads.GateLabel, beads.TrackLabel, beads.WorkstreamLabel} {
		bc := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-1.2", ParentID: "p-1", Labels: []string{kind}}}
		err := checkNotGateEdge(context.Background(), bc, "/p", "p-1.2", "internal-dep add", false)
		if err == nil || !strings.Contains(err.Error(), "cmdr internal-gate link --project <project> --bead") {
			t.Fatalf("incomplete command: %v", err)
		}
	}
}

func TestFiledMinorPromptAdvice(t *testing.T) {
	ownership, err := os.ReadFile("../../prompts/ownership.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ownership), "internal-gate add` again with `--criteria`") || !strings.Contains(string(ownership), "internal-create-objective --gate ... --criteria") {
		t.Fatal("gate advice must use supported flags")
	}
	lead, err := os.ReadFile("../../prompts/lead.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(lead), "internal-dep rm refuses gate and kind:track beads") {
		t.Fatal("missing edge-removal boundary")
	}
}
