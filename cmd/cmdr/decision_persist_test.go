package main

import (
	"context"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func decisionPersistCard(t *testing.T, projectName, beadID string) (*store.NeedsYou, *store.Store, int64, func()) {
	t.Helper()
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.GetProjectByName(context.Background(), projectName)
	if err != nil || p == nil {
		cleanup()
		t.Fatalf("project: %v", err)
	}
	card, err := st.GetNeedsYou(context.Background(), p.ID, beadID)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	return card, st, p.ID, cleanup
}

func TestDecisionPersist_ExistingRowGetsCardWithoutAsk(t *testing.T) {
	setupObjectiveProject(t, &beads.MockClient{})
	ctx := context.Background()
	_, st, pid, cleanup := decisionPersistCard(t, "manifest", "ms-1.4")
	defer cleanup()
	if err := st.UpsertNeedsYou(ctx, store.NeedsYou{ProjectID: pid, BeadID: "ms-1.4", Ask: "Approve: gate"}); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, read := withTempOutputFiles(t)
	args := []string{"--project", "manifest", "--bead", "ms-1.4", "--state", "needs-user", "--no-notify", "--decision", testDecision}
	if code := runInternalSetObjective(args, stdout, stderr); code != 0 {
		_, errOut := read()
		t.Fatalf("exit=%d %s", code, errOut)
	}
	got, _ := st.GetNeedsYou(ctx, pid, "ms-1.4")
	if got == nil || !strings.Contains(got.Decision, "Merge #4107?") || got.Ask != "Merge #4107?" {
		t.Fatalf("card not stored with the question as ask: %+v", got)
	}
}

func TestDecisionPersist_SyncWithNothingIsANoop(t *testing.T) {
	setupObjectiveProject(t, &beads.MockClient{})
	ctx := context.Background()
	_, st, pid, cleanup := decisionPersistCard(t, "manifest", "ms-1.4")
	defer cleanup()
	if err := st.UpsertNeedsYou(ctx, store.NeedsYou{ProjectID: pid, BeadID: "ms-1.4", Ask: "Keep me", Decision: `{"question":"Old?"}`}); err != nil {
		t.Fatal(err)
	}
	if err := syncNeedsYou(ctx, "manifest", "ms-1.4", beads.StateNeedsUser, "", "", "", nil); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetNeedsYou(ctx, pid, "ms-1.4")
	if got == nil || got.Ask != "Keep me" || got.Decision != `{"question":"Old?"}` {
		t.Fatalf("row changed: %+v", got)
	}
}

func TestDecisionPersist_SyncDerivesAskFromDecisionQuestion(t *testing.T) {
	setupObjectiveProject(t, &beads.MockClient{})
	ctx := context.Background()
	_, st, pid, cleanup := decisionPersistCard(t, "manifest", "ms-1.4")
	defer cleanup()
	if err := st.UpsertNeedsYou(ctx, store.NeedsYou{ProjectID: pid, BeadID: "ms-1.4", Ask: "Approve: gate"}); err != nil {
		t.Fatal(err)
	}
	if err := syncNeedsYou(ctx, "manifest", "ms-1.4", beads.StateNeedsUser, "", "", `{"question":"Ship it?","options":[{"label":"Yes"}]}`, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetNeedsYou(ctx, pid, "ms-1.4")
	if got == nil || got.Ask != "Ship it?" || !strings.Contains(got.Decision, "Ship it?") {
		t.Fatalf("card not stored: %+v", got)
	}
}

func TestDecisionPersist_InvalidDecisionRefused(t *testing.T) {
	for name, card := range map[string]string{
		"bad json":       `{"question":`,
		"empty question": `{"question":"  ","options":[{"label":"Yes"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			setupObjectiveProject(t, &beads.MockClient{})
			stdout, stderr, read := withTempOutputFiles(t)
			args := []string{"--project", "manifest", "--bead", "ms-1.4", "--state", "needs-user", "--no-notify", "--decision", card}
			if code := runInternalSetObjective(args, stdout, stderr); code == 0 {
				t.Fatal("an invalid card must be refused")
			}
			if _, errOut := read(); errOut == "" {
				t.Fatal("no error text")
			}
			got, _, _, cleanup := decisionPersistCard(t, "manifest", "ms-1.4")
			defer cleanup()
			if got != nil {
				t.Fatalf("a row was stored: %+v", got)
			}
		})
	}
}

func TestDecisionPersist_ReflagReplacesCardAndKeepsHistory(t *testing.T) {
	setupObjectiveProject(t, &beads.MockClient{})
	ctx := context.Background()
	_, st, pid, cleanup := decisionPersistCard(t, "manifest", "ms-1.4")
	defer cleanup()
	for _, card := range []string{testDecision, `{"question":"Second?","options":[{"label":"Go"}]}`} {
		stdout, stderr, read := withTempOutputFiles(t)
		args := []string{"--project", "manifest", "--bead", "ms-1.4", "--state", "needs-user", "--no-notify", "--decision", card}
		if code := runInternalSetObjective(args, stdout, stderr); code != 0 {
			_, errOut := read()
			t.Fatalf("exit=%d %s", code, errOut)
		}
	}
	got, _ := st.GetNeedsYou(ctx, pid, "ms-1.4")
	if got == nil || got.Ask != "Second?" || !strings.Contains(got.Decision, "Second?") {
		t.Fatalf("card not replaced: %+v", got)
	}
	pitches, err := st.ListPitches(ctx, pid, "ms-1.4")
	if err != nil || len(pitches) != 2 {
		t.Fatalf("history: %v %+v", err, pitches)
	}
}

func TestDecisionPersist_OutputSaysStoredThenReplaced(t *testing.T) {
	setupObjectiveProject(t, &beads.MockClient{})
	for _, want := range []string{`"card":"stored"`, `"card":"replaced"`} {
		stdout, stderr, read := withTempOutputFiles(t)
		args := []string{"--project", "manifest", "--bead", "ms-1.4", "--state", "needs-user", "--no-notify", "--decision", testDecision}
		if code := runInternalSetObjective(args, stdout, stderr); code != 0 {
			_, errOut := read()
			t.Fatalf("exit=%d %s", code, errOut)
		}
		if out, _ := read(); !strings.Contains(out, want) {
			t.Fatalf("want %s in %s", want, out)
		}
	}
}

func TestDecisionPersist_ParkThenUnparkKeepsTheCard(t *testing.T) {
	t.Setenv("COMMANDER_ROLE", "manager-ws")
	m := parkFixture(t)
	stdout, stderr, read := withTempOutputFiles(t)
	flag := []string{"--project", "manifest", "--bead", "obj-1", "--state", "needs-user", "--no-notify", "--decision", testDecision}
	if code := runInternalSetObjective(flag, stdout, stderr); code != 0 {
		_, e := read()
		t.Fatalf("flag: %s", e)
	}
	for _, f := range []string{"--park", "--unpark"} {
		if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "obj-1", f}, stdout, stderr); code != 0 {
			t.Fatalf("%s failed", f)
		}
	}
	_ = m
	got, _, _, cleanup := decisionPersistCard(t, "manifest", "obj-1")
	defer cleanup()
	if got == nil || !strings.Contains(got.Decision, "Merge #4107?") {
		t.Fatalf("card lost: %+v", got)
	}
}
