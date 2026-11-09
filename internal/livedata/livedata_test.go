package livedata

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/gates"
	"github.com/example-user/commander/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// Invented Manager pane: no busy marker, but the background-agent
// panel lists two running sub-agents (same fixture as the TUI's).
const subAgentPane = `The catalog checks are delegated.

❯
  ⏺ main
  ◯ general-purpose  Checking catalog import rows                    3m 12s · ↓ 8.2k tokens
  ◯ general-purpose  Checking catalog export order                   2m 8s · ↓ 6.1k tokens`

func TestClassifyPane(t *testing.T) {
	cases := []struct{ pane, state, detail string }{
		{"✻ Cooked for 1m\n\n✻ Waiting for 1 background agent to finish\n❯", store.AgentWaiting, "1 sub-agent"},
		{"✻ Brewing… (12s · esc to interrupt)\n❯", store.AgentWorking, ""},
		{"✻ Cooked for 2m 3s\n❯", store.AgentIdle, ""},
		{subAgentPane, store.AgentWaiting, "2 sub-agents"},
	}
	for _, c := range cases {
		if s, d := ClassifyPane(c.pane); s != c.state || d != c.detail {
			t.Errorf("%q: got %s/%q, want %s/%q", c.pane, s, d, c.state, c.detail)
		}
	}
}

func TestScrapeAgentStates_ReadsTaggedChatPanes(t *testing.T) {
	old := execCommand
	t.Cleanup(func() { execCommand = old })
	execCommand = func(name string, args ...string) *exec.Cmd {
		if args[0] == "list-panes" {
			return exec.Command("printf", "%%1|1|ws-a|manager (p)\n%%2|1||commander\n%%3|||commander\n")
		}
		if args[3] == "%1" {
			return exec.Command("printf", "✻ Brewing (esc to interrupt)")
		}
		return exec.Command("printf", "✻ Cooked for 3s")
	}
	got := ScrapeAgentStates(time.Unix(100, 0))
	if len(got) != 2 || got["manager-ws-a"].State != store.AgentWorking || got["commander"].State != store.AgentIdle {
		t.Fatalf("got %+v", got)
	}
}

func TestEffectiveState_SilentWorkingIsIdle(t *testing.T) {
	now := time.Unix(10_000, 0)
	fresh := store.AgentState{State: store.AgentWorking, Updated: now.Unix() - 5}
	stale := store.AgentState{State: store.AgentWorking, Updated: now.Unix() - 3600}
	stuck := store.AgentState{State: store.AgentStuck, Updated: now.Unix() - 3600}
	if EffectiveState(fresh, now) != store.AgentWorking || EffectiveState(stale, now) != store.AgentIdle || EffectiveState(stuck, now) != store.AgentStuck {
		t.Fatal("effective state")
	}
}

func TestMergeAgentStates_HooksWinUnlessStale(t *testing.T) {
	now := time.Unix(10_000, 0)
	hooks := map[string]store.AgentState{
		"fresh": {State: store.AgentWorking, Detail: "Edit: x", Updated: now.Unix() - 3},
		"stale": {State: store.AgentIdle, Updated: now.Unix() - 9000},
	}
	scraped := map[string]store.AgentState{
		"fresh": {State: store.AgentIdle},
		"stale": {State: store.AgentWorking},
	}
	got := MergeAgentStates(hooks, scraped, now)
	if got["fresh"].Detail != "Edit: x" || got["stale"].State != store.AgentWorking {
		t.Fatalf("got %+v", got)
	}
}

// fleet is two projects with ranked workstreams and a phase > unit step tree.
func fleet() Data {
	return Data{
		Projects: []store.Project{{ID: 1, Name: "p1"}, {ID: 2, Name: "p2"}},
		Objectives: []ObjectiveRow{
			{BeadID: "a", Title: "WS A", ProjectID: 1, CreatedAt: "1"},
			{BeadID: "a.2", ParentBeadID: "a", Title: "Go live", ProjectID: 1, State: beads.StateNotStarted},
			{BeadID: "a.1", ParentBeadID: "a", Title: "Build", ProjectID: 1, State: beads.StateInProgress},
			{BeadID: "a.1.1", ParentBeadID: "a.1", Title: "U1 spike", ProjectID: 1, State: beads.StateDone, Status: "closed"},
			{BeadID: "a.1.2", ParentBeadID: "a.1", Title: "U2 decide", ProjectID: 1, State: beads.StateNeedsUser,
				Ask: "Pick a DB", Decision: &store.Decision{Question: "Pick a DB", Options: []store.DecisionOption{{Label: "sqlite", Recommended: true}}}},
			{BeadID: "a.1.3", ParentBeadID: "a.1", Title: "U3 worker", ProjectID: 1, State: beads.StateInProgress, Review: "42"},
			{BeadID: "a.2.1", ParentBeadID: "a.2", Title: "Deploy", ProjectID: 1, State: beads.StateNotStarted},
			{BeadID: "b", Title: "WS B", ProjectID: 2, CreatedAt: "2"},
		},
		Ranks:    map[store.WorkstreamKey]int{{ProjectID: 2, BeadID: "b"}: 0},
		LoadedAt: time.Unix(10_000, 0),
	}
}

func TestWorkstreams_RankOrderThenCreation(t *testing.T) {
	got := Workstreams(fleet())
	if len(got) != 2 || got[0].Root.BeadID != "b" || got[1].Root.BeadID != "a" {
		t.Fatalf("order = %+v", got)
	}
	var ids []string
	for _, s := range got[1].Steps {
		ids = append(ids, s.Row.BeadID)
	}
	if strings.Join(ids, ",") != "a.1,a.1.1,a.1.2,a.1.3,a.2,a.2.1" {
		t.Fatalf("steps in plan order = %v", ids)
	}
}

func TestStepTree_NowStepAndPhaseNotes(t *testing.T) {
	roots := BuildStepTree(Workstreams(fleet())[1].Steps)
	if len(roots) != 2 || len(roots[0].Children) != 3 {
		t.Fatalf("tree = %+v", roots)
	}
	if got := NowStep(roots); got != "a.1.3" {
		t.Errorf("now = %q", got)
	}
	if got := PhaseNote(roots[0]); got != "⚑ needs you" {
		t.Errorf("phase note = %q", got)
	}
	if got := PhaseNote(roots[1]); got != "not started" {
		t.Errorf("phase note = %q", got)
	}
}

func TestNeedsYouList_CarriesTheDecisionCard(t *testing.T) {
	got := NeedsYouList(fleet())
	if len(got) != 1 || got[0].BeadID != "a.1.2" || got[0].Rank != 2 || got[0].Decision == nil || got[0].Decision.Options[0].Label != "sqlite" {
		t.Fatalf("got %+v", got)
	}
}

func TestNeedsYouList_UnapprovedWorkflowGateIsADecisionCard(t *testing.T) {
	d := fleet()
	d.Objectives = append(d.Objectives,
		ObjectiveRow{BeadID: "a.3", ParentBeadID: "a", Title: "Workflow approved", ProjectID: 1, State: beads.StateNotStarted,
			Gate: true, Approvers: []string{"user"}, GateNote: "Stages: build, ship."},
		ObjectiveRow{BeadID: "a.4", ParentBeadID: "a", Title: "Reviewer sign-off", ProjectID: 1, State: beads.StateNotStarted,
			Gate: true, Approvers: []string{"someone"}},
		ObjectiveRow{BeadID: "a.5", ParentBeadID: "a", Title: "Old gate", ProjectID: 1, State: beads.StateDone, Status: "closed",
			Gate: true, Approvers: []string{"user"}},
	)
	for i := range d.Objectives {
		if d.Objectives[i].BeadID == "a.1" || d.Objectives[i].BeadID == "a.2" {
			d.Objectives[i].BlockedBy = []string{"a.3"} // the workflow gate comes before its stages
		}
	}
	got := NeedsYouList(d)
	if len(got) != 2 || got[1].BeadID != "a.3" {
		t.Fatalf("only the open user gate joins the queue: %+v", got)
	}
	card := got[1].Decision
	if card == nil || card.Context != "Stages: build, ship." || len(card.Options) != 3 ||
		card.Options[0].Label != beads.GateApprove || card.Options[1].Label != beads.GateChange || card.Options[2].Label != beads.GateDrop {
		t.Fatalf("card = %+v", card)
	}
}

func TestNeedsYouList_GateWaitsForAnEarlierOpenSibling(t *testing.T) {
	// The recurring shape: a Greenlight gate with no blocked_by behind an open deploy stage.
	d := fleet()
	d.Objectives = append(d.Objectives,
		ObjectiveRow{BeadID: "a.3", ParentBeadID: "a", Title: "Greenlight: run the reconcile", ProjectID: 1, State: beads.StateNotStarted,
			Gate: true, Approvers: []string{"user"}, GateNote: "The pitch."},
	)
	for _, n := range NeedsYouList(d) {
		if n.BeadID == "a.3" {
			t.Fatalf("greenlight surfaced while earlier stages are open: %+v", n)
		}
	}
	for i := range d.Objectives {
		if d.Objectives[i].BeadID == "a.1" || d.Objectives[i].BeadID == "a.2" {
			d.Objectives[i].Status, d.Objectives[i].State = "closed", beads.StateDone
		}
	}
	found := false
	for _, n := range NeedsYouList(d) {
		found = found || n.BeadID == "a.3"
	}
	if !found {
		t.Fatal("greenlight should surface once the earlier stages close")
	}
}

func TestNeedsYouList_GateWaitsForAnEarlierOpenStage(t *testing.T) {
	d := fleet()
	d.Objectives = append(d.Objectives,
		ObjectiveRow{BeadID: "a.6", ParentBeadID: "a", Title: "PR approval", ProjectID: 1, State: beads.StateNotStarted,
			Gate: true, Approvers: []string{"reviewer-a"}},
		ObjectiveRow{BeadID: "a.7", ParentBeadID: "a", Title: "Merge the stack", ProjectID: 1, State: beads.StateNotStarted,
			Gate: true, Approvers: []string{"user"}, BlockedBy: []string{"a.6"}, GateNote: "The pitch."},
	)
	for _, n := range NeedsYouList(d) {
		if n.BeadID == "a.7" {
			t.Fatalf("merge ask surfaced while PR approval is open: %+v", n)
		}
	}
	for i := range d.Objectives {
		if id := d.Objectives[i].BeadID; id == "a.6" || id == "a.1" || id == "a.2" {
			d.Objectives[i].Status, d.Objectives[i].State = "closed", beads.StateDone
		}
	}
	found := false
	for _, n := range NeedsYouList(d) {
		found = found || n.BeadID == "a.7"
	}
	if !found {
		t.Fatal("merge ask should surface once the earlier stage closes")
	}
}

func TestBuildSnapshot_Shape(t *testing.T) {
	d := fleet()
	d.Agents = map[string]store.AgentState{
		"manager-a": {State: store.AgentWorking, Detail: "Bash", Since: 9_000, Updated: 9_999},
	}
	s := BuildSnapshot(d, map[string]int{"commander": 3})
	if len(s.Workstreams) != 2 || s.Workstreams[1].N != 2 || s.Workstreams[1].NowStep != "a.1.3" || s.Workstreams[1].ManagerRole != "manager-a" {
		t.Fatalf("workstreams = %+v", s.Workstreams)
	}
	build := s.Workstreams[1].Steps[0]
	if build.PhaseNote != "⚑ needs you" || len(build.Children) != 3 || !build.Children[2].Now || build.Children[2].Review != "42" {
		t.Fatalf("build phase = %+v", build)
	}
	if build.Children[0].State != beads.StateDone || build.Children[1].Decision == nil {
		t.Fatalf("children = %+v", build.Children)
	}
	if s.Workstreams[1].Done != 1 || s.Workstreams[1].Total != 6 {
		t.Fatalf("progress = %d/%d", s.Workstreams[1].Done, s.Workstreams[1].Total)
	}
	if s.Agents["manager-a"].State != store.AgentWorking || s.Unread["commander"] != 3 || len(s.NeedsYou) != 1 {
		t.Fatalf("snapshot = %+v", s)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"version"`, `"generated_at"`, `"needs_you"`, `"workstreams"`, `"children"`, `"now":true`, `"phase_note"`, `"unread"`, `"fleet"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("json lacks %s", key)
		}
	}
}

func TestLoad_FromStoreAndBeadsAttachesDecisions(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	pid, err := st.InsertProject(ctx, store.Project{Name: "ops", Type: "code", Path: t.TempDir(), ActivatedAt: 1, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	card := `{"question":"Ship it?","options":[{"label":"yes","recommended":true},{"label":"no"}]}`
	if err := st.UpsertNeedsYou(ctx, store.NeedsYou{ProjectID: pid, BeadID: "w.1", Ask: "verify", Decision: card, Since: 1_700_000_000}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAgentState(ctx, "manager-w", store.AgentWorking, "Edit: x"); err != nil {
		t.Fatal(err)
	}
	bc := &beads.MockClient{ListResult: []beads.Bead{
		{ID: "w", Title: "Workstream", Status: "open", CreatedAt: "1"},
		{ID: "w.1", Title: "Decide", Status: "open", Labels: []string{"kind:objective", "needs-user", "pushed:4", "pushed-first:1700000000"}},
		{ID: "w.2", Title: "Review", Status: "in_progress", Labels: []string{"kind:objective", "in-review", "pr:7"}},
	}}
	d := Load(ctx, Deps{Store: st, Beads: bc})
	if d.LoadErr != nil {
		t.Fatal(d.LoadErr)
	}
	s := BuildSnapshot(d, nil)
	if len(s.NeedsYou) != 1 || s.NeedsYou[0].Ask != "Ship it?" || s.NeedsYou[0].Decision == nil || len(s.NeedsYou[0].Decision.Options) != 2 {
		t.Fatalf("needs you = %+v", s.NeedsYou)
	}
	if s.NeedsYou[0].PushCount != 4 || s.NeedsYou[0].PushFirst != 1700000000 || s.Workstreams[0].Steps[0].PushCount != 4 {
		t.Fatalf("push labels missing from snapshot: %+v", s.NeedsYou[0])
	}
	if s.NeedsYou[0].Since != 1_700_000_000 {
		t.Fatalf("needs you since = %d, want the ask's raise time", s.NeedsYou[0].Since)
	}
	if raw, _ := json.Marshal(s.NeedsYou[0]); !strings.Contains(string(raw), `"since":1700000000`) {
		t.Fatalf("needs you json lacks since: %s", raw)
	}
	if s.Workstreams[0].Steps[1].Review != "7" || s.Agents["manager-w"].Detail != "Edit: x" || len(s.Projects) != 1 {
		t.Fatalf("snapshot = %+v", s)
	}
}

func TestLoad_NoStore(t *testing.T) {
	if d := Load(context.Background(), Deps{}); d.LoadErr != ErrNoStore {
		t.Fatalf("err = %v", d.LoadErr)
	}
}

func TestSource_ThrottlesUnreadMail(t *testing.T) {
	calls := 0
	src := &Source{
		Deps:        Deps{Store: openStore(t)},
		UnreadEvery: time.Hour,
		Unread: func(_ context.Context, role string) (int, error) {
			calls++
			return 2, nil
		},
	}
	first := src.Snapshot(context.Background())
	src.Snapshot(context.Background())
	if calls != 1 || first.Unread["commander"] != 2 {
		t.Fatalf("calls = %d, unread = %+v", calls, first.Unread)
	}
}

// A hung mail server cannot hold the snapshot (the app's start screen waits
// on it): each role's count is bounded, and a role that times out keeps the
// count it had.
func TestSource_SlowMailDoesNotHoldTheSnapshot(t *testing.T) {
	slow := false
	src := &Source{
		Deps: Deps{Store: openStore(t)},
		Unread: func(ctx context.Context, role string) (int, error) {
			if slow {
				<-ctx.Done()
				return 0, ctx.Err()
			}
			return 4, nil
		},
	}
	src.Snapshot(context.Background())
	slow = true
	t0 := time.Now()
	snap := src.Snapshot(context.Background())
	if d := time.Since(t0); d > unreadTimeout+time.Second {
		t.Fatalf("snapshot took %s with a hung mail server", d)
	}
	if snap.Unread["commander"] != 4 {
		t.Fatalf("unread = %+v, want commander's last count (4) kept", snap.Unread)
	}
}

func TestWire_RoundTripsDataThroughJSON(t *testing.T) {
	d := Data{
		Projects:   []store.Project{{ID: 1, Name: "p"}},
		Objectives: []ObjectiveRow{{BeadID: "w", Title: "WS", ProjectName: "p", ProjectID: 1, State: "in-progress"}},
		Ranks:      map[store.WorkstreamKey]int{{ProjectID: 1, BeadID: "w"}: 3},
		Agents:     map[string]store.AgentState{"manager-w": {Role: "manager-w", State: store.AgentWorking, Detail: "Bash: go test"}},
		LoadErr:    errors.New("br busy"),
	}
	raw, err := json.Marshal(ToWire(d))
	if err != nil {
		t.Fatal(err)
	}
	var w Wire
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatal(err)
	}
	got := FromWire(w)
	if got.Ranks[store.WorkstreamKey{ProjectID: 1, BeadID: "w"}] != 3 || got.Agents["manager-w"].Detail != "Bash: go test" ||
		len(got.Objectives) != 1 || got.LoadErr == nil || got.LoadErr.Error() != "br busy" {
		t.Fatalf("round trip lost data: %+v", got)
	}
}

func TestBuildSnapshot_ShowsWhatAManagerWaitsOn(t *testing.T) {
	d := fleet()
	waits := beads.WaitsOf([]string{"waiting:9500:ci:4101", "waiting:9600:review:reviewer-a", "waiting:9700:review:reviewer-b"})
	for i := range d.Objectives {
		if d.Objectives[i].BeadID == "a.1.3" {
			d.Objectives[i].Waits = waits
		}
	}
	d.Agents = map[string]store.AgentState{"manager-a": {State: store.AgentIdle, Since: 9_000, Updated: 9_999}}
	s := BuildSnapshot(d, nil)
	a := s.Agents["manager-a"]
	if a.State != store.AgentWaiting || a.Wait != "CI #4101 · review (reviewer-a, reviewer-b)" || a.Since != 9500 {
		t.Fatalf("agent = %+v", a)
	}
	if got := s.Workstreams[1].Steps[0].Children[2].Waiting; got != a.Wait {
		t.Fatalf("step waiting = %q", got)
	}

	// A working Manager keeps working, with the wait alongside.
	d.Agents["manager-a"] = store.AgentState{State: store.AgentWorking, Since: 9_000, Updated: 9_999}
	if a := BuildSnapshot(d, nil).Agents["manager-a"]; a.State != store.AgentWorking || a.Wait == "" {
		t.Fatalf("working agent = %+v", a)
	}

	// No waits, nothing changes.
	d.Agents["manager-a"] = store.AgentState{State: store.AgentIdle, Updated: 9_999}
	d.Objectives = fleet().Objectives
	if a := BuildSnapshot(d, nil).Agents["manager-a"]; a.State != store.AgentIdle || a.Wait != "" {
		t.Fatalf("no waits: %+v", a)
	}
}

func TestLoad_AttachesGatesAndFlagsWorkstreamsWithNone(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	pid, err := st.InsertProject(ctx, store.Project{Name: "ops", Type: "code", Path: t.TempDir(), ActivatedAt: 1, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	pid2, err := st.InsertProject(ctx, store.Project{Name: "other", Type: "code", Path: t.TempDir(), ActivatedAt: 1, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertGate(ctx, store.Gate{ProjectID: pid, BeadID: "w.1", Name: "unit", Check: "go test ./...", Verifier: "auto"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGateResult(ctx, pid, "w.1", "unit", store.GatePassed, "ok", "auto", time.Unix(70, 0)); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertGate(ctx, store.Gate{ProjectID: pid, BeadID: "w.1", Name: "signoff", Check: "ship?", Verifier: "user"}); err != nil {
		t.Fatal(err)
	}
	bc := &beads.MockClient{ListResult: []beads.Bead{
		{ID: "w", Title: "Gated", Status: "open", CreatedAt: "1"},
		{ID: "w.1", Title: "Stage one", Status: "open", Labels: []string{"kind:objective"}},
		{ID: "w.2", Title: "Stage two", Status: "open", Labels: []string{"kind:objective"}},
	}}
	// pid2 shares bead ids and must not see ops's gates.
	_ = pid2
	d := Load(ctx, Deps{Store: st, Beads: bc})
	if d.LoadErr != nil {
		t.Fatal(d.LoadErr)
	}
	s := BuildSnapshot(d, nil)
	var ops *SnapWorkstream
	for i := range s.Workstreams {
		if s.Workstreams[i].Project == "ops" {
			ops = &s.Workstreams[i]
		}
	}
	if ops == nil {
		t.Fatalf("no ops workstream in %+v", s.Workstreams)
	}
	if ops.NoGates {
		t.Error("a workstream with gated stages is flagged no gates")
	}
	one, two := ops.Steps[0], ops.Steps[1]
	if len(one.Gates) != 2 || one.Gates[0] != (SnapGate{Name: "unit", Verifier: "auto", Status: "passed", Evidence: "ok", By: "auto", At: 70}) ||
		one.Gates[1].Status != "open" || one.Gates[1].Verifier != "user" || one.NoGates {
		t.Errorf("stage one = %+v", one)
	}
	if len(two.Gates) != 0 || !two.NoGates {
		t.Errorf("stage two = %+v", two)
	}
	raw, _ := json.Marshal(one)
	if !strings.Contains(string(raw), `"gates":[{"name":"unit","verifier":"auto","status":"passed","evidence":"ok","by":"auto","at":70}`) {
		t.Errorf("json = %s", raw)
	}

	// The gates survive the wire form the sidebar columns use.
	var w Wire
	b, _ := json.Marshal(ToWire(d))
	if err := json.Unmarshal(b, &w); err != nil {
		t.Fatal(err)
	}
	if got := BuildSnapshot(FromWire(w), nil).Workstreams[0].Steps[0].Gates; len(got) != 2 {
		t.Errorf("wire lost gates: %+v", got)
	}

	// With no gates anywhere the workstream is flagged.
	if err := st.DeleteGate(ctx, pid, "w.1", "unit"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteGate(ctx, pid, "w.1", "signoff"); err != nil {
		t.Fatal(err)
	}
	s = BuildSnapshot(Load(ctx, Deps{Store: st, Beads: bc}), nil)
	if !s.Workstreams[0].NoGates {
		t.Errorf("workstream without gates not flagged: %+v", s.Workstreams[0])
	}
}

func TestLoad_GateBeadsAreStageGatesAndGraphNodes(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	dir := t.TempDir()
	pid, err := st.InsertProject(ctx, store.Project{Name: "ops", Type: "code", Path: dir, ActivatedAt: 1, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	mem := &beads.MemClient{Prefix: "w"}
	mem.Seed(beads.Bead{ID: "w", Title: "Gated", CreatedAt: "1", Labels: []string{"kind:objective"}})
	mem.Seed(beads.Bead{ID: "w.1", Title: "Stage one", Labels: []string{"kind:objective"}})
	mem.Seed(beads.Bead{ID: "w.2", Title: "Stage two", Labels: []string{"kind:objective"}, DependencyCount: 2})
	mem.Edges = append(mem.Edges, beads.Dep{IssueID: "w.2", DependsOnID: "w.1", Type: "blocks"}, beads.Dep{IssueID: "w.2", DependsOnID: "w", Type: "parent-child"})

	unit, err := gates.Upsert(ctx, mem, dir, store.Gate{ProjectID: pid, BeadID: "w.1", Name: "unit", Check: "go test ./...", Verifier: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	n := gates.Node{ID: unit, Gate: store.Gate{Status: store.GateOpen}}
	if err := gates.SetResult(ctx, mem, dir, n, store.GatePassed, "ok", "auto", time.Unix(70, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := gates.Upsert(ctx, mem, dir, store.Gate{ProjectID: pid, BeadID: "w.1", Name: "signoff", Check: "ship?", Verifier: "user"}); err != nil {
		t.Fatal(err)
	}
	// A gates-table row nobody migrated yet still shows (on stage two).
	if err := st.UpsertGate(ctx, store.Gate{ProjectID: pid, BeadID: "w.2", Name: "old", Check: "x", Verifier: "operator"}); err != nil {
		t.Fatal(err)
	}

	d := Load(ctx, Deps{Store: st, Beads: mem})
	if d.LoadErr != nil {
		t.Fatal(d.LoadErr)
	}
	ws := BuildSnapshot(d, nil).Workstreams[0]
	var one, two SnapStep
	for _, step := range ws.Steps {
		if step.BeadID == "w.1" {
			one = step
		}
		if step.BeadID == "w.2" {
			two = step
		}
	}
	if len(one.Gates) != 2 || one.Gates[0].Name != "unit" || one.Gates[0].Status != "passed" || one.Gates[0].Evidence != "ok" ||
		one.Gates[0].By != "auto" || one.Gates[0].At != 70 || one.Gates[0].BeadID != unit || one.Gates[1].Status != "open" || one.NoGates {
		t.Errorf("stage one gates = %+v", one.Gates)
	}
	if len(two.Gates) != 1 || two.Gates[0].Name != "old" || two.Gates[0].BeadID != "" {
		t.Errorf("unmigrated gate = %+v", two.Gates)
	}
	if len(ws.GateNodes) != 2 {
		t.Fatalf("gate nodes = %+v", ws.GateNodes)
	}
	g := ws.GateNodes[0]
	if g.Kind != "gate" || g.BeadID != unit || g.Stage != "w.1" || g.Name != "unit" || g.Status != "passed" || g.Check != "go test ./..." ||
		len(g.BlockedBy) != 1 || g.BlockedBy[0] != "w.1" || len(g.Blocks) != 1 || g.Blocks[0] != "w.2" {
		t.Errorf("gate node = %+v", g)
	}
	raw, _ := json.Marshal(g)
	if g.Viz != 1 || one.Viz != 3 || !strings.Contains(string(raw), `"viz":1`) {
		t.Errorf("viz: gate node %d, stage step %d", g.Viz, one.Viz)
	}
	if !strings.Contains(string(raw), `"kind":"gate"`) || !strings.Contains(string(raw), `"blocked_by":["w.1"]`) || !strings.Contains(string(raw), `"blocks":["w.2"]`) {
		t.Errorf("json = %s", raw)
	}
	// Approval gates appear beside the two stages; auto checks stay graph nodes.
	if len(ws.Steps) != 3 {
		t.Errorf("gate beads leaked into the step tree: %+v", ws.Steps)
	}
}

func TestLoad_RetiredGatesAreHidden(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	dir := t.TempDir()
	pid, err := st.InsertProject(ctx, store.Project{Name: "ops", Type: "code", Path: dir, ActivatedAt: 1, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	mem := &beads.MemClient{Prefix: "w"}
	mem.Seed(beads.Bead{ID: "w", Title: "Gated", CreatedAt: "1", Labels: []string{"kind:objective"}})
	mem.Seed(beads.Bead{ID: "w.1", Title: "Stage one", Labels: []string{"kind:objective"}})
	gateLabels := func(name string, extra ...string) []string {
		return append(gates.Labels(store.Gate{BeadID: "w.1", Name: name, Verifier: "user"}), extra...)
	}
	mem.Seed(beads.Bead{ID: "w.g1", Status: "closed", CloseReason: "Superseded by w.g3", Labels: gateLabels("old")})
	mem.Seed(beads.Bead{ID: "w.g2", Status: "closed", Labels: gateLabels("dropped", gates.RetiredLabel)})
	mem.Seed(beads.Bead{ID: "w.g3", Status: "open", Labels: gateLabels("real")})

	d := Load(ctx, Deps{Store: st, Beads: mem})
	ws := BuildSnapshot(d, nil).Workstreams[0]
	var stage SnapStep
	for _, step := range ws.Steps {
		if step.BeadID == "w.1" {
			stage = step
		}
	}
	if got := stage.Gates; len(got) != 1 || got[0].Name != "real" {
		t.Errorf("stage gates = %+v", got)
	}
	if len(ws.GateNodes) != 1 || ws.GateNodes[0].Name != "real" {
		t.Errorf("gate nodes = %+v", ws.GateNodes)
	}
	_ = pid
}

func TestSource_UnreadableStoreIsDegraded(t *testing.T) {
	st := openStore(t)
	src := &Source{Deps: Deps{Store: st}}
	if snap := src.Snapshot(context.Background()); snap.Degraded != nil || src.LastLoadErr() != nil {
		t.Fatalf("healthy store reported degraded: %+v, %v", snap.Degraded, src.LastLoadErr())
	}
	st.Close()
	snap := src.Snapshot(context.Background())
	if snap.Degraded == nil || snap.Degraded.Reason == "" {
		t.Fatalf("closed store not degraded: %+v", snap)
	}
	if src.LastLoadErr() == nil {
		t.Fatal("LastLoadErr hidden by the kept good data")
	}
	if _, ok := src.Last(); !ok {
		t.Fatal("good data should be kept")
	}
}

func TestBuildSnapshot_TimeWaitCarriesItsTarget(t *testing.T) {
	d := fleet()
	for i := range d.Objectives {
		if d.Objectives[i].BeadID == "a.1.3" {
			d.Objectives[i].Waits = beads.WaitsOf([]string{"waiting:9500:until:20000:Nightly_20reconcile"})
		}
	}
	st := BuildSnapshot(d, nil).Workstreams[1].Steps[0].Children[2]
	if st.Waiting != "Nightly reconcile" || st.WaitUntil != 20000 || st.WaitUntilAs != "Nightly reconcile" || st.WaitSince != 9500 {
		t.Fatalf("step = %+v", st)
	}
}

// A rank written to state.db shows in the next Last/Snapshot with no bead change.
func TestWorkstreamRanks_ReadAfterWriteWithoutBeadChange(t *testing.T) {
	st := openStore(t)
	src := &Source{Deps: Deps{Store: st}}
	ctx := context.Background()
	src.Snapshot(ctx)
	key := store.WorkstreamKey{ProjectID: 1, BeadID: "x"}
	if err := st.SetWorkstreamOrder(ctx, []store.WorkstreamKey{key}); err != nil {
		t.Fatal(err)
	}
	d, ok := src.Last()
	if !ok || d.Ranks[key] != 1 {
		t.Fatalf("Last ranks = %v (ok %v)", d.Ranks, ok)
	}
}

func TestReviewPRAndRepo(t *testing.T) {
	cross := []string{"in-review", "pr:example-org/infrastructure#4003"}
	if got := ReviewPR(cross); got != "4003" {
		t.Errorf("ReviewPR = %q", got)
	}
	if got := ReviewRepo(cross); got != "example-org/infrastructure" {
		t.Errorf("ReviewRepo = %q", got)
	}
	own := []string{"in-review", "pr:4170"}
	if ReviewPR(own) != "4170" || ReviewRepo(own) != "" {
		t.Errorf("own repo: %q %q", ReviewPR(own), ReviewRepo(own))
	}
}
