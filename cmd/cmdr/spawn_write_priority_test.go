package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// busyClient answers the first createBusy Create calls and the first
// statusBusy SetStatus calls with the queue's busy error, then works.
type busyClient struct {
	*beads.MockClient
	createBusy, statusBusy int
	creates, statuses      int
}

var errQueueBusy = fmt.Errorf("br is busy (3 waiting, write not started); retry in a minute: %w", beads.ErrBusy)

func (b *busyClient) Create(ctx context.Context, dir, title string, o beads.CreateOptions) (string, error) {
	b.creates++
	if b.creates <= b.createBusy {
		return "", errQueueBusy
	}
	return b.MockClient.Create(ctx, dir, title, o)
}

func (b *busyClient) SetStatus(ctx context.Context, dir, id, status string) error {
	b.statuses++
	if b.statuses <= b.statusBusy {
		return errQueueBusy
	}
	return b.MockClient.SetStatus(ctx, dir, id, status)
}

func fastSpawnBackoff(t *testing.T) {
	old := spawnStepBackoff
	spawnStepBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	oldRetry := spawnRetryAfter
	spawnRetryAfter = time.Millisecond
	t.Cleanup(func() { spawnStepBackoff, spawnRetryAfter = old, oldRetry })
}

func spawnWith(t *testing.T, c beads.Client) (int, string) {
	t.Helper()
	orig := newBeadsClientFn
	newBeadsClientFn = func() beads.Client { return c }
	t.Cleanup(func() { newBeadsClientFn = orig })
	stdout, stderr, read := withTempOutputFiles(t)
	code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", "ms-1.2", "--title", "t"}, stdout, stderr)
	_, errOut := read()
	return code, errOut
}

func TestWritePrioritySpawnRetriesCreateOnQueueTimeoutAndContinues(t *testing.T) {
	fastSpawnBackoff(t)
	sp := &fakeOperatorSpawner{}
	stubSpawnDeps(t, sp, 0, 3)
	mock := &beads.MockClient{ShowResults: map[string]*beads.Bead{"ms-1.2": {ID: "ms-1.2", Status: "open"}}, CreateID: "ms-1.2.1", ListResult: []beads.Bead{approvedWorkflowGate}}
	c := &busyClient{MockClient: mock, createBusy: 2, statusBusy: 1}
	code, errOut := spawnWith(t, c)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	if c.creates != 3 || c.statuses != 2 {
		t.Fatalf("creates=%d statuses=%d; want 3 and 2", c.creates, c.statuses)
	}
	if len(sp.reqs) != 1 || sp.reqs[0].TaskBeadID != "ms-1.2.1" {
		t.Fatalf("one spawn on the one task: %+v", sp.reqs)
	}
	if strings.Contains(strings.Join(mock.Calls, "\n"), "Delete:") {
		t.Fatalf("a busy retry must not delete the task:\n%v", mock.Calls)
	}
}

func TestWritePrioritySpawnGivesUpAfterThreeTriesWithAFriendlyError(t *testing.T) {
	fastSpawnBackoff(t)
	sp := &fakeOperatorSpawner{}
	stubSpawnDeps(t, sp, 0, 3)
	mock := &beads.MockClient{ShowResults: map[string]*beads.Bead{"ms-1.2": {ID: "ms-1.2", Status: "open"}}, CreateID: "x", ListResult: []beads.Bead{approvedWorkflowGate}}
	c := &busyClient{MockClient: mock, createBusy: 99}
	stubMail(t)
	code, errOut := spawnWith(t, c)
	if code == 0 || !strings.Contains(errOut, "retry in a minute") {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	if c.creates < 3 || len(sp.reqs) != 0 {
		t.Fatalf("creates=%d spawns=%d", c.creates, len(sp.reqs))
	}
}

// lostCreate is a create whose answer never comes back; lands says whether the
// bead was written all the same.
type lostCreate struct {
	*beads.MockClient
	err     error
	lands   bool
	creates int
}

func (l *lostCreate) Create(_ context.Context, _, _ string, o beads.CreateOptions) (string, error) {
	l.creates++
	if l.lands {
		l.ListResult = append(l.ListResult, beads.Bead{ID: "ms-1.2.7", Title: "t", Labels: o.Labels, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
	}
	return "", l.err
}

// A create that may have landed is never repeated: a whole second attempt, the
// old behaviour on these errors, made a second bead with no owner.
func TestSpawnCreateThatMayHaveLandedIsNeverRepeated(t *testing.T) {
	for name, cerr := range map[string]error{
		"lost answer": fmt.Errorf("write door: EOF (%w)", beads.ErrMaybeApplied),
		"killed":      errors.New("br create: signal: killed"),
		"timed out":   fmt.Errorf("br create: %w", context.DeadlineExceeded),
	} {
		t.Run(name+", landed", func(t *testing.T) {
			fastSpawnBackoff(t)
			fastLandedLookup(t)
			sp := &fakeOperatorSpawner{}
			stubSpawnDeps(t, sp, 0, 3)
			c := &lostCreate{MockClient: &beads.MockClient{ShowResults: map[string]*beads.Bead{"ms-1.2": {ID: "ms-1.2", Status: "open"}}, ListResult: []beads.Bead{approvedWorkflowGate}}, err: cerr, lands: true}
			code, errOut := spawnWith(t, c)
			if code != 0 || c.creates != 1 || len(sp.reqs) != 1 || sp.reqs[0].TaskBeadID != "ms-1.2.7" {
				t.Fatalf("exit=%d creates=%d spawns=%+v stderr=%s", code, c.creates, sp.reqs, errOut)
			}
		})
		t.Run(name+", not visible", func(t *testing.T) {
			fastSpawnBackoff(t)
			fastLandedLookup(t)
			sp := &fakeOperatorSpawner{}
			stubSpawnDeps(t, sp, 0, 3)
			mailer := stubMail(t)
			c := &lostCreate{MockClient: &beads.MockClient{ShowResults: map[string]*beads.Bead{"ms-1.2": {ID: "ms-1.2", Status: "open"}}, ListResult: []beads.Bead{approvedWorkflowGate}}, err: cerr}
			code, errOut := spawnWith(t, c)
			if code != 1 || c.creates != 1 || len(sp.reqs) != 0 || len(mailer.sent) != 1 || !strings.Contains(errOut, "not repeated") {
				t.Fatalf("exit=%d creates=%d spawns=%d mails=%d stderr=%s", code, c.creates, len(sp.reqs), len(mailer.sent), errOut)
			}
		})
	}
}

// A create that bd plainly failed (locked) left nothing behind: one more attempt
// is still right.
func TestSpawnCreateRefusedForAPassingReasonStillRepeats(t *testing.T) {
	fastSpawnBackoff(t)
	fastLandedLookup(t)
	sp := &fakeOperatorSpawner{}
	stubSpawnDeps(t, sp, 0, 3)
	stubMail(t)
	c := &lostCreate{MockClient: &beads.MockClient{ShowResults: map[string]*beads.Bead{"ms-1.2": {ID: "ms-1.2", Status: "open"}}, ListResult: []beads.Bead{approvedWorkflowGate}}, err: errors.New("br create: database is locked")}
	if code, errOut := spawnWith(t, c); code != 1 || c.creates != 2 {
		t.Fatalf("exit=%d creates=%d stderr=%s", code, c.creates, errOut)
	}
}

type undeletable struct {
	*beads.MockClient
	creates int
}

func (u *undeletable) Create(ctx context.Context, dir, title string, o beads.CreateOptions) (string, error) {
	u.creates++
	return u.MockClient.Create(ctx, dir, title, o)
}

func (u *undeletable) Delete(context.Context, string, string, string) error {
	return errors.New("br delete: signal: killed")
}

// The spawn failed for a passing reason, and the task it made cannot be removed:
// another attempt would leave two.
func TestSpawnCreateLeftBehindEndsTheRepeats(t *testing.T) {
	fastSpawnBackoff(t)
	sp := &failFirst{n: 2, err: errors.New("br show: signal: killed")}
	stubSpawnDepsWith(t, sp)
	stubMail(t)
	u := &undeletable{MockClient: &beads.MockClient{ShowResults: map[string]*beads.Bead{"ms-1.2": {ID: "ms-1.2", Status: "open"}}, CreateID: "ms-1.2.1", ListResult: []beads.Bead{approvedWorkflowGate}}}
	code, errOut := spawnWith(t, u)
	if code != 1 || u.creates != 1 || len(sp.reqs) != 1 || !strings.Contains(errOut, "could not be removed") {
		t.Fatalf("exit=%d creates=%d spawns=%d stderr=%s", code, u.creates, len(sp.reqs), errOut)
	}
}

// sameTitleSpawns is the beads of two spawns under one step: each create lands
// or not as its turn says, and only the listed turns lose their answer.
type sameTitleSpawns struct {
	*beads.MockClient
	lost, lands map[int]bool
	creates     int
	shows       map[string]*beads.Bead
	showErr     error
}

func (c *sameTitleSpawns) Create(_ context.Context, _, title string, o beads.CreateOptions) (string, error) {
	c.creates++
	id := fmt.Sprintf("ms-1.2.%d", c.creates)
	if !c.lost[c.creates] || c.lands[c.creates] {
		c.ListResult = append(c.ListResult, beads.Bead{ID: id, Title: title, Labels: o.Labels, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
	}
	if c.lost[c.creates] {
		return "", fmt.Errorf("write door: EOF (%w)", beads.ErrMaybeApplied)
	}
	return id, nil
}

func (c *sameTitleSpawns) Show(ctx context.Context, dir, id string) (*beads.Bead, error) {
	if strings.HasPrefix(id, "ms-1.2.") {
		if c.showErr != nil {
			return nil, c.showErr
		}
		return c.shows[id], nil
	}
	return c.MockClient.Show(ctx, dir, id)
}

func (c *sameTitleSpawns) deleted() bool {
	return strings.Contains(strings.Join(c.Calls, "\n"), "Delete:")
}

func twoSpawns(lost, lands map[int]bool) *sameTitleSpawns {
	return &sameTitleSpawns{MockClient: &beads.MockClient{ShowResults: map[string]*beads.Bead{"ms-1.2": {ID: "ms-1.2", Status: "open"}}, ListResult: []beads.Bead{approvedWorkflowGate}}, lost: lost, lands: lands}
}

// A second spawn loses its create's answer while the first one's task of the
// same title is seconds old. It takes nothing and removes nothing: the first
// task has no owner yet in that window, so only the marker tells them apart.
func TestTwoSameTitleSpawnsNeverTakeEachOthersTask(t *testing.T) {
	fastSpawnBackoff(t)
	fastLandedLookup(t)
	sp := &fakeOperatorSpawner{}
	stubSpawnDeps(t, sp, 0, 9)
	mailer := stubMail(t)
	c := twoSpawns(map[int]bool{2: true}, nil)
	if code, errOut := spawnWith(t, c); code != 0 {
		t.Fatalf("first spawn: exit=%d stderr=%s", code, errOut)
	}
	code, errOut := spawnWith(t, c)
	if code != 1 || !strings.Contains(errOut, "not repeated") {
		t.Fatalf("second spawn: exit=%d stderr=%s", code, errOut)
	}
	if len(sp.reqs) != 1 || sp.reqs[0].TaskBeadID != "ms-1.2.1" || c.deleted() || len(mailer.sent) != 1 {
		t.Fatalf("spawns=%+v mails=%d calls:\n%v", sp.reqs, len(mailer.sent), c.Calls)
	}
}

// Both creates landed: the one whose answer was lost finds its own bead beside
// the other of the same title, and a failed start removes only that one.
func TestTwoSameTitleSpawnsAdoptOnlyTheirOwn(t *testing.T) {
	fastSpawnBackoff(t)
	fastLandedLookup(t)
	stubMail(t)
	sp := &failFirst{n: 1, err: errors.New("spawn: session exists")}
	stubSpawnDepsWith(t, sp)
	c := twoSpawns(map[int]bool{1: true}, map[int]bool{1: true})
	c.ListResult = append(c.ListResult, beads.Bead{ID: "ms-1.2.9", Title: "t", Labels: []string{"kind:task", "spawn-attempt:other"}, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
	code, errOut := spawnWith(t, c)
	if code != 1 || len(sp.reqs) != 1 || sp.reqs[0].TaskBeadID != "ms-1.2.1" {
		t.Fatalf("exit=%d spawns=%+v stderr=%s", code, sp.reqs, errOut)
	}
	calls := strings.Join(c.Calls, "\n")
	if !strings.Contains(calls, "Delete:ms-1.2.1") || strings.Contains(calls, "Delete:ms-1.2.9") {
		t.Fatalf("calls:\n%s", calls)
	}
}

func TestSpawnNeverRemovesOrAdoptsAHeldTask(t *testing.T) {
	owned := map[string]*beads.Bead{"ms-1.2.1": {ID: "ms-1.2.1", Labels: []string{"owner:op-x"}}}
	running := func(context.Context, string) (string, error) { return "op-y", nil }
	for _, c := range []struct {
		name      string
		shows     map[string]*beads.Bead
		showErr   error
		runningOn func(context.Context, string) (string, error)
		want      string
	}{
		{name: "owner", shows: owned, want: "owned by op-x"},
		{name: "running Operator", runningOn: running, want: "op-y running"},
		{name: "unreadable", showErr: errors.New("br show: exit 1"), want: "could not be read"},
		{name: "run table unreadable", runningOn: func(context.Context, string) (string, error) { return "", errors.New("db locked") }, want: "running Operator"},
	} {
		deps := func(t *testing.T, sp *failFirst) {
			t.Helper()
			orig := spawnOperatorDeps
			dir := t.TempDir()
			spawnOperatorDeps = func(string) (*operatorSpawnDeps, error) {
				return &operatorSpawnDeps{Spawner: sp, Project: &store.Project{ID: 7, Name: "manifest", Type: "code", Path: dir}, RunningOn: c.runningOn}, nil
			}
			t.Cleanup(func() { spawnOperatorDeps = orig })
		}
		t.Run(c.name+", start failed", func(t *testing.T) {
			fastSpawnBackoff(t)
			mailer := stubMail(t)
			sp := &failFirst{n: 9, err: errors.New("br show: signal: killed")}
			deps(t, sp)
			b := twoSpawns(nil, nil)
			b.shows, b.showErr = c.shows, c.showErr
			code, errOut := spawnWith(t, b)
			if code != 1 || b.deleted() || b.creates != 1 || !strings.Contains(errOut, c.want) {
				t.Fatalf("exit=%d creates=%d stderr=%s calls:\n%v", code, b.creates, errOut, b.Calls)
			}
			if len(mailer.sent) != 1 || !strings.Contains(mailer.sent[0].body, "ms-1.2.1") {
				t.Fatalf("the mail names the task left behind: %+v", mailer.sent)
			}
		})
		t.Run(c.name+", answer lost", func(t *testing.T) {
			fastSpawnBackoff(t)
			fastLandedLookup(t)
			stubMail(t)
			sp := &failFirst{n: 9, err: errors.New("unused")}
			deps(t, sp)
			b := twoSpawns(map[int]bool{1: true}, map[int]bool{1: true})
			b.shows, b.showErr = c.shows, c.showErr
			code, errOut := spawnWith(t, b)
			if code != 1 || len(sp.reqs) != 0 || b.deleted() || !strings.Contains(errOut, c.want) {
				t.Fatalf("exit=%d spawns=%d stderr=%s calls:\n%v", code, len(sp.reqs), errOut, b.Calls)
			}
		})
	}
}

// One try outlasts the door's worst case.
func TestSpawnStepOutwaitsTheDoor(t *testing.T) {
	if spawnStepTimeout < beadWriteTimeout {
		t.Fatalf("spawnStepTimeout %s < beadWriteTimeout %s", spawnStepTimeout, beadWriteTimeout)
	}
}
