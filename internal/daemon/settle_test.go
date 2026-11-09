package daemon

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
)

type oneProject struct{}

func (oneProject) GetProjectByID(context.Context, int64) (*store.Project, error) {
	return &store.Project{ID: 3, Path: "/p"}, nil
}

type sentMail struct{ to, subject string }

func noRunning(context.Context) ([]store.AgentRun, error) { return nil, nil }

func settler(b *beads.MockClient, sent *[]sentMail) runSettler {
	return runSettler{projects: oneProject{}, beads: b, running: noRunning, comments: reportComments("Done: all green\nmore detail"), mail: func(_ context.Context, to, subject, _ string) error {
		*sent = append(*sent, sentMail{to, subject})
		return nil
	}}
}

func reportComments(text string) func(context.Context, string, string) ([]beadComment, error) {
	return func(context.Context, string, string) ([]beadComment, error) {
		return []beadComment{
			{Text: "old note from a previous run", CreatedAt: "2020-01-01T00:00:00Z"},
			{Text: text, CreatedAt: "2030-01-01T00:00:00Z"},
		}, nil
	}
}

var run = store.AgentRun{ID: 9, ProjectID: 3, TaskBeadID: "p-ws.2.1", AgentName: "operator-x"}

// An Operator that finished hands its bead to the workstream Manager for review.
func TestSettleExitedPutsTheBeadInReview(t *testing.T) {
	b := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "in_progress", Labels: []string{"kind:task", "owner:operator-x"}}}
	var sent []sentMail
	if err := settler(b, &sent).exited(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(b.Calls, "\n")
	for _, want := range []string{"label=owner:operator-x|on=false", "label=owner:manager-p-ws|on=true", "label=review|on=true", "SetStatus:p-ws.2.1"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing %q in:\n%s", want, calls)
		}
	}
	if len(sent) != 1 || sent[0].to != "manager-p-ws" || sent[0].subject != "[operator] operator-x finished p-ws.2.1: Done: all green" {
		t.Fatalf("mail: %+v", sent)
	}
	if st, owner := beads.StepState("in_progress", []string{"owner:manager-p-ws", "review"}); st != beads.StateReview || owner != "manager-p-ws" {
		t.Fatalf("state=%s owner=%s", st, owner)
	}
}

// An Operator that died leaves its bead open with nobody on it.
func TestSettleDiedReopensTheBead(t *testing.T) {
	b := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "in_progress", Labels: []string{"owner:operator-x"}}}
	var sent []sentMail
	if err := settler(b, &sent).died(context.Background(), run, false); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(b.Calls, "\n")
	if !strings.Contains(calls, "label=owner:operator-x|on=false") || strings.Count(calls, "on=true") != 1 || !strings.Contains(calls, "label="+beads.OperatorDiedLabel+"|on=true") {
		t.Fatalf("calls:\n%s", calls)
	}
	if len(sent) != 1 || !strings.Contains(sent[0].subject, "Operator died") {
		t.Fatalf("mail: %+v", sent)
	}
}

// A bead its Manager already closed is left alone.
func TestSettleLeavesAClosedBeadAlone(t *testing.T) {
	b := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "closed"}}
	var sent []sentMail
	if err := settler(b, &sent).exited(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	for _, c := range b.Calls {
		if strings.HasPrefix(c, "Set") {
			t.Fatalf("changed a closed bead: %v", b.Calls)
		}
	}
}

type fakeSpawner struct{ reqs []spawn.SpawnRequest }

func (f *fakeSpawner) Spawn(_ context.Context, req spawn.SpawnRequest) (*spawn.SpawnResult, error) {
	f.reqs = append(f.reqs, req)
	return &spawn.SpawnResult{AgentName: "operator-op-x"}, nil
}

type codeProject struct{}

func (codeProject) GetProjectByID(context.Context, int64) (*store.Project, error) {
	return &store.Project{ID: 3, Name: "p", Type: "code", Path: "/p"}, nil
}

func respawner(b *beads.MockClient, sp *fakeSpawner, sent *[]sentMail) runSettler {
	s := settler(b, sent)
	s.projects = codeProject{}
	s.spawner = sp
	s.model = "anthropic/claude-sonnet-4-5"
	s.branchOf = func(wt string) (string, error) {
		if wt != "/p/.worktrees/x" {
			return "", fmt.Errorf("unexpected worktree %s", wt)
		}
		return "op/x", nil
	}
	return s
}

// An Operator a restart killed is reopened and started again on its own
// branch, resuming its session, with a cap of two per bead.
func TestSettleDiedInARestartRespawns(t *testing.T) {
	b := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "in_progress", Labels: []string{"owner:operator-x"}}}
	var sent []sentMail
	sp := &fakeSpawner{}
	if err := respawner(b, sp, &sent).died(context.Background(), run, true); err != nil {
		t.Fatal(err)
	}
	if len(sp.reqs) != 1 || !sp.reqs[0].Resume || sp.reqs[0].Branch != "op/x" || sp.reqs[0].TaskBeadID != "p-ws.2.1" || sp.reqs[0].Model == "" {
		t.Fatalf("spawn requests: %+v", sp.reqs)
	}
	calls := strings.Join(b.Calls, "\n")
	for _, want := range []string{"label=auto-respawn:1|on=true", "label=owner:operator-op-x|on=true", "SetStatus:p-ws.2.1"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing %q in:\n%s", want, calls)
		}
	}
	if len(sent) != 1 || sent[0].to != "manager-p-ws" || !strings.Contains(sent[0].subject, "respawned") {
		t.Fatalf("mail: %+v", sent)
	}
}

// A worktree on a detached HEAD has no branch to restart on: git names it
// "HEAD", and spawning on that failed with "'HEAD' is not a valid branch name".
// The step goes straight back to the Manager, with the reason.
func TestSettleDiedOnDetachedHEADHandsBack(t *testing.T) {
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "one")
	gitIn(t, repo, "checkout", "-q", "--detach")
	branch, err := gitBranchOf(repo)
	if err != nil || branch != spawn.DetachedHEAD {
		t.Fatalf("gitBranchOf in a detached worktree = %q, %v", branch, err)
	}

	b := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "in_progress", Labels: []string{"owner:operator-x"}}}
	var sent []sentMail
	sp := &fakeSpawner{}
	s := respawner(b, sp, &sent)
	s.branchOf = func(string) (string, error) { return gitBranchOf(repo) }
	var bodies []string
	s.mail = func(_ context.Context, _, _, body string) error {
		bodies = append(bodies, body)
		return nil
	}
	if err := s.died(context.Background(), run, true); err != nil {
		t.Fatal(err)
	}
	if len(sp.reqs) != 0 {
		t.Fatalf("spawned on a detached HEAD: %+v", sp.reqs)
	}
	if len(bodies) != 1 || !strings.Contains(bodies[0], "Not restarted: its worktree is on a detached HEAD") {
		t.Fatalf("mail: %q", bodies)
	}
	if calls := strings.Join(b.Calls, "\n"); !strings.Contains(calls, "label="+beads.OperatorDiedLabel+"|on=true") {
		t.Fatalf("not marked died:\n%s", calls)
	}
}

// Restart deaths stop at two per bead; a crash that was already restarted once
// is not restarted again: both go back to the Manager.
func TestSettleDiedRespawnIsCapped(t *testing.T) {
	capped := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "in_progress", Labels: []string{"auto-respawn:1", "auto-respawn:2"}}}
	var sent []sentMail
	sp := &fakeSpawner{}
	if err := respawner(capped, sp, &sent).died(context.Background(), run, true); err != nil {
		t.Fatal(err)
	}
	crash := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "in_progress", Labels: []string{"crash-respawn:1793000000"}}}
	if err := respawner(crash, sp, &sent).died(context.Background(), run, false); err != nil {
		t.Fatal(err)
	}
	if len(sp.reqs) != 0 {
		t.Fatalf("respawned: %+v", sp.reqs)
	}
	if len(sent) != 2 || !strings.Contains(sent[0].subject, "Operator died") || !strings.Contains(sent[1].subject, "Operator died") {
		t.Fatalf("mail: %+v", sent)
	}
}

// A closed run freezes its worktree (exited or died); a respawn leaves it for spawn to unlock.
func TestSettleLocksTheClosedRunsWorktree(t *testing.T) {
	for name, settle := range map[string]func(runSettler) error{
		"exited": func(s runSettler) error { return s.exited(context.Background(), run) },
		"died":   func(s runSettler) error { return s.died(context.Background(), run, false) },
	} {
		b := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "in_progress", Labels: []string{"owner:operator-x"}}}
		var sent []sentMail
		s := settler(b, &sent)
		var locked []string
		s.lockWorktree = func(dir string) error { locked = append(locked, dir); return nil }
		if err := settle(s); err != nil {
			t.Fatal(err)
		}
		if len(locked) != 1 || locked[0] != "/p/.worktrees/x" {
			t.Errorf("%s: locked %v, want [/p/.worktrees/x]", name, locked)
		}
	}
}

// An Operator that exits without writing a report after it started did not
// finish: its bead is open again, nobody owns it, and the Manager is told.
func TestSettleExitedWithoutAReportIsDied(t *testing.T) {
	b := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "in_progress", Labels: []string{"owner:operator-x"}}}
	var sent []sentMail
	s := settler(b, &sent)
	s.comments = func(context.Context, string, string) ([]beadComment, error) {
		return []beadComment{{Text: "written before this run", CreatedAt: "2020-01-01T00:00:00Z"}}, nil
	}
	r := run
	r.SpawnTime = 1900000000
	if err := s.exited(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(b.Calls, "\n")
	if strings.Contains(calls, "label=review|on=true") || !strings.Contains(calls, "SetStatus:p-ws.2.1|status=open") {
		t.Fatalf("calls:\n%s", calls)
	}
	if len(sent) != 1 || !strings.Contains(sent[0].subject, "Operator died") {
		t.Fatalf("mail: %+v", sent)
	}
}

type recordedEvents struct{ got []store.CostEvent }

func (r *recordedEvents) InsertCostEvent(_ context.Context, e store.CostEvent) (int64, error) {
	r.got = append(r.got, e)
	return 1, nil
}

// How a run ended is recorded with the time and the report line, for the app.
func TestSettleRecordsHowTheRunEnded(t *testing.T) {
	b := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "in_progress", Labels: []string{"owner:operator-x"}}}
	var sent []sentMail
	ev := &recordedEvents{}
	s := settler(b, &sent)
	s.events = ev
	s.now = func() time.Time { return time.Unix(1234, 0) }
	if err := s.exited(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := s.died(context.Background(), run, false); err != nil {
		t.Fatal(err)
	}
	if len(ev.got) != 2 || ev.got[0].EventType != store.EventOperatorExited || ev.got[0].Reason.String != "Done: all green" || ev.got[0].OccurredAt != 1234 || ev.got[1].EventType != store.EventOperatorDied {
		t.Fatalf("events: %+v", ev.got)
	}
}

// Invented promotion: example-app-t02.8 becomes a workstream (no parent edge),
// so an Operator under it settles with manager-example-app-t02.8.
func TestSettleRoutesToThePromotedWorkstreamManager(t *testing.T) {
	b := &beads.MockClient{
		ShowResult:    &beads.Bead{ID: "example-app-t02.8.4.5", Status: "in_progress", Labels: []string{"kind:task", "owner:operator-x"}},
		ParentsResult: map[string]string{"example-app-t02.8": ""},
	}
	var sent []sentMail
	promoted := store.AgentRun{ID: 9, ProjectID: 3, TaskBeadID: "example-app-t02.8.4.5", AgentName: "operator-x"}
	if err := settler(b, &sent).exited(context.Background(), promoted); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(b.Calls, "\n"), "label=owner:"+beads.OwnerLabelName("manager-example-app-t02.8")+"|on=true") {
		t.Fatalf("bead not handed to the promoted Manager:\n%s", strings.Join(b.Calls, "\n"))
	}
	if len(sent) != 1 || sent[0].to != "manager-example-app-t02.8" {
		t.Fatalf("mail: %+v", sent)
	}
}

// lateShow serves a bead as it reads at each successive Show, then the last.
type lateShow struct {
	*beads.MockClient
	seq []*beads.Bead
	n   int
}

func (l *lateShow) Show(ctx context.Context, dir, id string) (*beads.Bead, error) {
	l.MockClient.Show(ctx, dir, id)
	i := l.n
	if i >= len(l.seq) {
		i = len(l.seq) - 1
	}
	l.n++
	return l.seq[i], nil
}

func noWrites(t *testing.T, calls []string) {
	t.Helper()
	for _, c := range calls {
		if strings.HasPrefix(c, "Set") && !strings.Contains(c, "on=false") {
			t.Fatalf("wrote onto a settled bead: %v", calls)
		}
	}
}

// The Manager closes the task between the settler's read and its writes: no
// review label, no Manager-to-close wait, no reopen.
func TestExitHandlerClosedMidSettleGetsNoLabels(t *testing.T) {
	open := &beads.Bead{ID: "p-ws.2.1", Status: "in_progress", Labels: []string{"owner:operator-x"}}
	closed := &beads.Bead{ID: "p-ws.2.1", Status: "closed", Labels: []string{"owner:operator-x"}}
	l := &lateShow{MockClient: &beads.MockClient{}, seq: []*beads.Bead{open, closed}}
	var sent []sentMail
	s := settler(l.MockClient, &sent)
	s.beads = l
	if err := s.exited(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	noWrites(t, l.Calls)
	for _, c := range l.Calls {
		if strings.HasPrefix(c, "SetStatus") || strings.Contains(c, "review") {
			t.Fatalf("touched a closed bead: %v", l.Calls)
		}
	}
}

// A closed task whose Operator exits without a report is not reopened.
func TestExitHandlerClosedTaskNotReopenedOnDeath(t *testing.T) {
	b := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "closed"}}
	var sent []sentMail
	if err := settler(b, &sent).died(context.Background(), run, false); err != nil {
		t.Fatal(err)
	}
	for _, c := range b.Calls {
		if strings.HasPrefix(c, "Set") {
			t.Fatalf("changed a closed bead: %v", b.Calls)
		}
	}
}

// A run that already handed its bead to the Manager (review) is not reopened
// by a late death notice, and a late exit adds nothing.
func TestExitHandlerFinishedRunNotReopened(t *testing.T) {
	w := beads.Wait{Kind: beads.WaitUntil, Since: time.Now(), Until: time.Now().Add(time.Hour), Ref: beads.ManagerCloseWait}
	review := &beads.Bead{ID: "p-ws.2.1", Status: "in_progress", Labels: []string{"owner:manager-p-ws", "review", w.Label()}}
	for name, f := range map[string]func(runSettler) error{
		"died":   func(s runSettler) error { return s.died(context.Background(), run, false) },
		"exited": func(s runSettler) error { return s.exited(context.Background(), run) },
	} {
		b := &beads.MockClient{ShowResult: review}
		var sent []sentMail
		if err := f(settler(b, &sent)); err != nil {
			t.Fatal(name, err)
		}
		for _, c := range b.Calls {
			if strings.HasPrefix(c, "Set") {
				t.Fatalf("%s: changed a finished bead: %v", name, b.Calls)
			}
		}
	}
}

// A task another Operator claimed after this run began is left alone.
func TestExitHandlerReclaimedTaskLeftAlone(t *testing.T) {
	b := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "in_progress", Labels: []string{"owner:operator-y"}}}
	var sent []sentMail
	if err := settler(b, &sent).died(context.Background(), run, false); err != nil {
		t.Fatal(err)
	}
	for _, c := range b.Calls {
		if strings.HasPrefix(c, "Set") {
			t.Fatalf("changed a reclaimed bead: %v", b.Calls)
		}
	}
}
