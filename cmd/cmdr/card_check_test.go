package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
)

const cardCheckReason = "read the new description, the card still holds"

// cardCheckFixture seeds a bead and an open card raised against basis.
func cardCheckFixture(t *testing.T, basis store.Basis, since int64) (*beads.MemClient, *store.Store, int64) {
	t.Helper()
	t.Setenv("COMMANDER_ROLE", "manager-ws")
	m := &beads.MemClient{}
	m.Seed(beads.Bead{ID: "ms-1", Title: "Learn", Description: "v2", Status: "open", Labels: []string{"kind:objective"}})
	setupObjectiveProject(t, m)
	_, st, pid, cleanup := decisionPersistCard(t, "manifest", "ms-1")
	t.Cleanup(cleanup)
	if err := st.UpsertNeedsYou(context.Background(), store.NeedsYou{ProjectID: pid, BeadID: "ms-1", Ask: "Q", Decision: `{"question":"Q?"}`, Since: since, Basis: &basis}); err != nil {
		t.Fatal(err)
	}
	return m, st, pid
}

func runCardCheckVerb(t *testing.T, reason string) (int, string) {
	t.Helper()
	stdout, stderr, read := withTempOutputFiles(t)
	args := []string{"--project", "manifest", "--bead", "ms-1", "--card-check", "still-right"}
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	code := runInternalSetObjective(args, stdout, stderr)
	_, e := read()
	return code, e
}

func TestCardCheck_SpecChangedRebaselinesAndComments(t *testing.T) {
	m, st, pid := cardCheckFixture(t, store.Basis{Spec: "stale-hash", Raised: time.Now().Unix()}, time.Now().Unix())
	before, _ := st.GetNeedsYou(context.Background(), pid, "ms-1")
	key := livedata.StaleNoticeKey(livedata.NeedsYou{Project: "manifest", BeadID: "ms-1", Basis: before.Basis})
	if code, e := runCardCheckVerb(t, cardCheckReason); code != 0 {
		t.Fatalf("exit %d %s", code, e)
	}
	b, _ := m.Show(nil, "", "ms-1")
	got, _ := st.GetNeedsYou(context.Background(), pid, "ms-1")
	if got.Basis.Spec != beads.SpecHash(*b) || got.Basis.AgeChecks != 0 {
		t.Fatalf("basis: %+v", got.Basis)
	}
	if next := livedata.StaleNoticeKey(livedata.NeedsYou{Project: "manifest", BeadID: "ms-1", Basis: got.Basis}); next == key {
		t.Fatal("real card-check did not renew stale notice key")
	}
	cs, _ := m.Comments(nil, "", "ms-1")
	if len(cs) != 1 || !strings.Contains(cs[0].Text, "still right") || !strings.Contains(cs[0].Text, cardCheckReason) {
		t.Fatalf("comment: %+v", cs)
	}
	if ps, _ := st.ListPitches(context.Background(), pid, "ms-1"); len(ps) != 1 {
		t.Fatalf("the card keeps its version: %d", len(ps))
	}
}

func TestCardCheck_AgeOnlyOnceThenRefused(t *testing.T) {
	old := time.Now().Add(-9 * 24 * time.Hour).Unix()
	m, st, pid := cardCheckFixture(t, store.Basis{Raised: old}, old)
	b, _ := m.Show(nil, "", "ms-1")
	// the basis carries the current spec: only age fired
	if err := st.RebaselineCard(context.Background(), pid, "ms-1", store.Basis{Spec: beads.SpecHash(*b), Raised: old}); err != nil {
		t.Fatal(err)
	}
	if code, e := runCardCheckVerb(t, cardCheckReason); code != 0 {
		t.Fatalf("first age check: %d %s", code, e)
	}
	got, _ := st.GetNeedsYou(context.Background(), pid, "ms-1")
	if got.Basis.AgeChecks != 1 || time.Now().Unix()-got.Basis.Raised > 60 {
		t.Fatalf("basis after first check: %+v", got.Basis)
	}
	// Age the card again: still the same spec, a second age-only check is refused.
	if err := st.RebaselineCard(context.Background(), pid, "ms-1", store.Basis{Spec: beads.SpecHash(*b), Raised: old, AgeChecks: 1}); err != nil {
		t.Fatal(err)
	}
	code, e := runCardCheckVerb(t, cardCheckReason)
	if code == 0 || !strings.Contains(e, "age alone") {
		t.Fatalf("second age-only check must be refused: %d %s", code, e)
	}
	// Editing the step resets the count.
	_ = m.UpdateFields(nil, "", "ms-1", beads.Fields{Title: ptr("Learn v3")})
	if code, e := runCardCheckVerb(t, cardCheckReason); code != 0 {
		t.Fatalf("after a spec change the check is allowed again: %d %s", code, e)
	}
	got, _ = st.GetNeedsYou(context.Background(), pid, "ms-1")
	if got.Basis.AgeChecks != 0 {
		t.Fatalf("age checks reset: %+v", got.Basis)
	}
}

func TestCardCheck_RefusesWhenNothingIsStaleOrUnderspecified(t *testing.T) {
	m, st, pid := cardCheckFixture(t, store.Basis{Raised: time.Now().Unix()}, time.Now().Unix())
	b, _ := m.Show(nil, "", "ms-1")
	_ = st.RebaselineCard(context.Background(), pid, "ms-1", store.Basis{Spec: beads.SpecHash(*b), Raised: time.Now().Unix()})
	if code, e := runCardCheckVerb(t, cardCheckReason); code == 0 || !strings.Contains(e, "not out of date") {
		t.Fatalf("a fresh card has nothing to confirm: %d %s", code, e)
	}
	if code, _ := runCardCheckVerb(t, "short"); code == 0 {
		t.Fatal("a reason under 15 characters must be refused")
	}
	if code, _ := runCardCheckVerb(t, ""); code == 0 {
		t.Fatal("a reason is required")
	}
	t.Setenv("COMMANDER_ROLE", "operator-x")
	if code, _ := runCardCheckVerb(t, cardCheckReason); code == 0 {
		t.Fatal("an Operator cannot confirm a card")
	}
}

func TestCardCheck_NoCardRefused(t *testing.T) {
	t.Setenv("COMMANDER_ROLE", "manager-ws")
	m := &beads.MemClient{}
	m.Seed(beads.Bead{ID: "ms-1", Status: "open"})
	setupObjectiveProject(t, m)
	if code, e := runCardCheckVerb(t, cardCheckReason); code == 0 || !strings.Contains(e, "no open card") {
		t.Fatalf("%d %s", code, e)
	}
}

func TestCardCheck_ReRaiseWritesANewVersionWithANewBasis(t *testing.T) {
	m, st, pid := cardCheckFixture(t, store.Basis{Spec: "stale-hash", Raised: time.Now().Unix()}, time.Now().Unix())
	stdout, stderr, read := withTempOutputFiles(t)
	args := []string{"--project", "manifest", "--bead", "ms-1", "--state", "needs-user", "--no-notify", "--decision", `{"question":"Q?","options":[{"label":"Yes"}]}`}
	if code := runInternalSetObjective(args, stdout, stderr); code != 0 {
		_, e := read()
		t.Fatalf("re-raise: %s", e)
	}
	b, _ := m.Show(nil, "", "ms-1")
	got, _ := st.GetNeedsYou(context.Background(), pid, "ms-1")
	ps, _ := st.ListPitches(context.Background(), pid, "ms-1")
	if len(ps) != 2 || got.Basis == nil || got.Basis.Spec != beads.SpecHash(*b) || got.Basis.Gen != 2 {
		t.Fatalf("versions=%d basis=%+v", len(ps), got.Basis)
	}
}

func ptr(s string) *string { return &s }

func TestCardCheck_DependencyChangedRebaselines(t *testing.T) {
	now := time.Now().Unix()
	m, st, pid := cardCheckFixture(t, store.Basis{Raised: now, PRs: map[string]string{"owner/repo#12": "OPEN"}}, now)
	b, _ := m.Show(nil, "", "ms-1")
	_ = st.RebaselineCard(context.Background(), pid, "ms-1", store.Basis{Spec: beads.SpecHash(*b), Raised: now, PRs: map[string]string{"owner/repo#12": "OPEN"}})
	stubBoard(t, &livedata.Snapshot{Projects: []livedata.SnapProject{{Name: "manifest", Repo: "owner/repo"}}, PRStates: map[string]string{"owner/repo#12": "MERGED"}})
	if code, e := runCardCheckVerb(t, cardCheckReason); code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
	got, _ := st.GetNeedsYou(context.Background(), pid, "ms-1")
	if len(got.Basis.PRs) != 0 {
		t.Fatalf("obsolete references retained: %+v", got.Basis)
	}
	if got.Basis.AgeChecks != 0 {
		t.Fatal("dependency check counted as age")
	}
}
