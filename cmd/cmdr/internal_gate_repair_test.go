package main

import (
	"context"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/gates"
)

func TestGatePublicationRepairCommand(t *testing.T) {
	for _, role := range []string{"manager-sample", "manager-other", "operator-sample"} {
		t.Run(role, func(t *testing.T) {
			m := setupGateProject(t)
			m.EdgeParentsOnly = true
			ctx := context.Background()
			m.Seed(beads.Bead{ID: "sample", Labels: []string{"kind:objective"}})
			m.Seed(beads.Bead{ID: "wrapper", Labels: []string{"kind:objective"}})
			m.SetParent(ctx, "/d", "wrapper", "sample")
			m.Seed(beads.Bead{ID: "action", Labels: []string{"kind:objective"}})
			m.SetParent(ctx, "/d", "action", "wrapper")
			m.Seed(beads.Bead{ID: "card", Labels: []string{"kind:gate", "gate-of:action", "approver:user", "verifier:user"}})
			m.DependOn(ctx, "/d", "wrapper", "card")
			m.DependOn(ctx, "/d", "action", "card")
			t.Setenv("COMMANDER_ROLE", role)
			args := []string{"publish", "--project", "manifest", "--bead", "card", "--parent", "wrapper", "--repair-ancestor-waits", "wrapper", "--reason", "Redundant ancestor wait"}
			out, errOut, read := withTempOutputFiles(t)
			code := runInternalGate(append(args, "--dry-run"), out, errOut)
			_, msg := read()
			allowed := role == "manager-sample"
			if (code == 0) != allowed {
				t.Fatalf("%d %s", code, msg)
			}
			ds, _ := m.Details(ctx, "/d", []string{"card"})
			if ds["card"].Parent != "" {
				t.Fatal("dry run wrote")
			}
			out, errOut, read = withTempOutputFiles(t)
			code = runInternalGate(args, out, errOut)
			_, msg = read()
			if (code == 0) != allowed {
				t.Fatalf("%d %s", code, msg)
			}
			ds, _ = m.Details(ctx, "/d", []string{"card"})
			if (ds["card"].Parent == "wrapper") != allowed {
				t.Fatal("incorrect mutation")
			}
		})
	}
}
func TestPublicationRepairAuditCannotBeForged(t *testing.T) {
	m := setupGateProject(t)
	m.Seed(beads.Bead{ID: "card"})
	out, errOut, read := withTempOutputFiles(t)
	code := runInternalComment([]string{"--project", "manifest", "--bead", "card", "--text", "\n " + gates.PublicationRepairPrefix + "{}"}, strings.NewReader(""), out, errOut)
	_, msg := read()
	if code == 0 || !strings.Contains(msg, "only by internal-gate publish") {
		t.Fatal(code, msg)
	}
}
