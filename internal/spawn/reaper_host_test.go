package spawn

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/example-user/commander/internal/docker"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/testguard"
	"github.com/example-user/commander/internal/tmuxctl"
)

// hostRunLister serves a fixed list of running runs to ReapHost.
type hostRunLister struct {
	runs []store.AgentRun
	err  error
}

func (h hostRunLister) ListRunningRuns(context.Context) ([]store.AgentRun, error) {
	return h.runs, h.err
}

func hostRun(id int64, session string) store.AgentRun {
	return store.AgentRun{
		ID: id, ProjectID: 3, TaskBeadID: "bd-9", AgentName: "operator-x", Status: "running",
		TmuxPaneID: sql.NullString{String: session, Valid: session != ""},
	}
}

func newHostReaper(fs *fakeStore, tx *tmuxctl.MockClient, runs []store.AgentRun, sessions []TmuxSession) *Reaper {
	r := NewReaper(fs, &docker.MockClient{}).WithHost(tx, hostRunLister{runs: runs})
	r.now = func() time.Time { return time.Unix(1700000000, 0) }
	r.listSessions = func(context.Context) ([]TmuxSession, error) { return sessions, nil }
	r.exited = exitedSince
	return r
}

func TestReapHost_ClosesRunsWhoseSessionIsGone(t *testing.T) {
	fs := newFakeStore()
	tx := &tmuxctl.MockClient{}
	old := time.Unix(1700000000, 0).Add(-time.Hour)
	dockerRun := store.AgentRun{ID: 3, Status: "running", ContainerID: sql.NullString{String: "c1", Valid: true}}
	r := newHostReaper(fs, tx,
		[]store.AgentRun{hostRun(1, "cmdr-p-alive"), hostRun(2, "cmdr-p-dead"), dockerRun},
		[]TmuxSession{{Name: "cmdr-p-alive", Created: old}},
	)

	res, err := r.ReapHost(context.Background())
	if err != nil {
		t.Fatalf("ReapHost: %v", err)
	}
	if len(res.ClosedRuns) != 1 || res.ClosedRuns[0] != 2 || len(res.KilledSessions) != 0 {
		t.Fatalf("result: %+v", res)
	}
	if fs.closed[2] != "orphan-reaped" || len(fs.closed) != 1 {
		t.Errorf("closed: %v (docker runs and live sessions must be left alone)", fs.closed)
	}
	if len(fs.costEvents) != 1 || fs.costEvents[0].EventType != "orphan-reaped" || fs.costEvents[0].AgentName != "operator-x" {
		t.Errorf("cost events: %+v", fs.costEvents)
	}
	if len(tx.Calls) != 0 {
		t.Errorf("no tmux kill expected: %v", tx.Calls)
	}
}

func TestReapHost_KillsOldUnclaimedSessionsOnly(t *testing.T) {
	fs := newFakeStore()
	tx := &tmuxctl.MockClient{}
	now := time.Unix(1700000000, 0)
	r := newHostReaper(fs, tx,
		[]store.AgentRun{hostRun(1, "cmdr-p-tracked")},
		[]TmuxSession{
			{Name: "cmdr-p-tracked", Created: now.Add(-time.Hour)},
			{Name: "cmdr-p-orphan", Created: now.Add(-time.Hour)},
			{Name: "cmdr-p-spawning", Created: now.Add(-time.Minute)}, // spawn in flight: no row yet
			{Name: "commander", Created: now.Add(-time.Hour)},         // the cockpit: never ours to kill
		},
	)

	res, err := r.ReapHost(context.Background())
	if err != nil {
		t.Fatalf("ReapHost: %v", err)
	}
	if len(res.KilledSessions) != 1 || res.KilledSessions[0] != "cmdr-p-orphan" {
		t.Fatalf("killed: %+v", res)
	}
	if len(tx.Calls) != 1 || tx.Calls[0] != "KillPane(target=cmdr-p-orphan)" {
		t.Errorf("tmux calls: %v", tx.Calls)
	}
	if len(fs.costEvents) != 1 || fs.costEvents[0].EventType != "orphan-reaped" {
		t.Errorf("cost events: %+v", fs.costEvents)
	}
}

func TestReapHost_ListErrorChangesNothing(t *testing.T) {
	fs := newFakeStore()
	tx := &tmuxctl.MockClient{}
	r := newHostReaper(fs, tx, []store.AgentRun{hostRun(1, "cmdr-p-a")}, nil)
	r.listSessions = func(context.Context) ([]TmuxSession, error) { return nil, errors.New("tmux exploded") }

	if _, err := r.ReapHost(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	if len(fs.closed) != 0 || len(tx.Calls) != 0 {
		t.Errorf("a failed listing must not close runs: %v %v", fs.closed, tx.Calls)
	}
}

func TestReapHost_RequiresWithHost(t *testing.T) {
	if _, err := NewReaper(newFakeStore(), &docker.MockClient{}).ReapHost(context.Background()); err == nil {
		t.Fatal("expected a configuration error")
	}
}

// An Operator mid Bash tool call has bash in the foreground, but its pane
// process still has children: it must not look finished.
func TestParseTmuxPanes_ShellWithChildrenIsBusy(t *testing.T) {
	oldAlive, oldKids := pidAlive, pidHasChildren
	pidAlive = func(int) bool { return true }
	pidHasChildren = func(pid int) bool { return pid == 14 }
	defer func() { pidAlive, pidHasChildren = oldAlive, oldKids }()
	got := parseTmuxPanes("cmdr-p-tool|1700000000|0|bash|14\ncmdr-p-idle|1700000000|0|bash|15\n")
	if len(got) != 2 || got[0].Idle || !got[1].Idle {
		t.Errorf("mid-tool-call bash must be busy, childless bash idle: %+v", got)
	}
}

func TestParseTmuxPanes(t *testing.T) {
	old := pidAlive
	pidAlive = func(pid int) bool { return pid != 999 }
	defer func() { pidAlive = old }()
	got := parseTmuxPanes("cmdr-p-a|1700000000|0|zsh|10\ncmdr-p-a|1700000000|0|claude|11\ncmdr-p-b|1700000001|0|zsh|12\ncmdr-p-c|1700000002|1|claude|13\ncmdr-p-d|1700000003|0|kernel_task|999\nbad line\nx|NaN|0|zsh|1\n")
	if len(got) != 4 || got[0].Name != "cmdr-p-a" || got[0].Created.Unix() != 1700000000 {
		t.Fatalf("parsed: %+v", got)
	}
	if got[0].Idle || !got[1].Idle || !got[2].Idle || !got[3].Idle {
		t.Errorf("idle flags (live claude, bare shell, dead pane, gone pid): %+v", got)
	}
}

// A session whose command exited on a real tmux server (private socket) is
// reported idle: the pane pid is gone even if tmux still lists the session.
func TestListTmuxSessions_ExitedCommandIsIdle(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux")
	}
	t.Setenv("TMUX", "")
	dir, err := os.MkdirTemp("/tmp", "cl.")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("TMUX_TMPDIR", dir)
	defer testguard.KillTmuxServer(dir, "default")
	for _, c := range [][]string{
		{"new-session", "-d", "-s", "cmdr-t-live", "sleep", "30"},
		{"new-session", "-d", "-s", "cmdr-t-gone", "-x", "80", "-y", "24", "true"},
		{"set-option", "-g", "remain-on-exit", "on"},
	} {
		if out, err := exec.Command("tmux", c...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %v: %v %s", c, err, out)
		}
	}
	time.Sleep(500 * time.Millisecond)
	got, err := listTmuxSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	idle := map[string]bool{}
	for _, s := range got {
		idle[s.Name] = s.Idle
	}
	if idle["cmdr-t-live"] {
		t.Errorf("a live command must not be idle: %v", idle)
	}
	if gone, ok := idle["cmdr-t-gone"]; ok && !gone {
		t.Errorf("an exited command must be idle: %v", idle)
	}
}

// A session back at a shell means the Operator finished: the run is exited,
// not an orphan, and no orphan event is recorded.
func TestReapHost_RunWhoseSessionIsOnlyAShellExited(t *testing.T) {
	fs := newFakeStore()
	tx := &tmuxctl.MockClient{}
	r := newHostReaper(fs, tx, []store.AgentRun{hostRun(7, "cmdr-p-done")},
		[]TmuxSession{{Name: "cmdr-p-done", Created: time.Unix(1700000000, 0).Add(-time.Hour), Idle: true}})
	res, err := r.ReapHost(context.Background())
	if err != nil || len(res.Exited) != 1 || res.Exited[0].ID != 7 || len(res.Died) != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if fs.closed[7] != "exited" || len(fs.costEvents) != 0 {
		t.Fatalf("closed=%v events=%+v", fs.closed, fs.costEvents)
	}
}

// A just-spawned Operator's pane is a shell until claude starts: not exited.
func TestReapHost_LeavesAYoungRunAtAShellAlone(t *testing.T) {
	fs := newFakeStore()
	tx := &tmuxctl.MockClient{}
	run := hostRun(8, "cmdr-p-new")
	run.SpawnTime = time.Unix(1700000000, 0).Add(-20 * time.Second).Unix()
	r := newHostReaper(fs, tx, []store.AgentRun{run},
		[]TmuxSession{{Name: "cmdr-p-new", Created: time.Unix(1700000000, 0).Add(-20 * time.Second), Idle: true}})
	res, err := r.ReapHost(context.Background())
	if err != nil || len(res.ClosedRuns) != 0 || len(fs.closed) != 0 {
		t.Fatalf("res=%+v closed=%v err=%v", res, fs.closed, err)
	}
}

// The spawn cap counts past dead Operators without closing their runs: closing
// there skipped cmdrd's settle, so a dead Operator's bead stayed in progress
// and its Manager was never told.
func TestLiveRunningRunsSkipsDeadWithoutClosing(t *testing.T) {
	fs := newFakeStore()
	tx := &tmuxctl.MockClient{}
	old := time.Unix(1700000000, 0).Add(-time.Hour)
	r := newHostReaper(fs, tx,
		[]store.AgentRun{hostRun(1, "cmdr-p-alive"), hostRun(2, "cmdr-p-dead")},
		[]TmuxSession{{Name: "cmdr-p-alive", Created: old}},
	)
	live, err := r.LiveRunningRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].ID != 1 {
		t.Fatalf("live runs: %+v", live)
	}
	if len(fs.closed) != 0 || len(fs.costEvents) != 0 {
		t.Fatalf("counting must not close or record anything: closed %v, events %v", fs.closed, fs.costEvents)
	}
}

// The board learns of a finished run before the sweep: the same call the sweep
// makes (back at a shell, or gone with an exit marker, and no stop asked for),
// with nothing closed and the marker left for the sweep.
func TestFinishedRunningRunsMatchTheSweepAndChangeNothing(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	fs := newFakeStore()
	old := time.Unix(1700000000, 0).Add(-time.Hour)
	runs := []store.AgentRun{
		hostRun(1, "cmdr-p-working"), hostRun(2, "cmdr-p-shell"), hostRun(3, "cmdr-p-marked"), hostRun(4, "cmdr-p-killed"), hostRun(5, "cmdr-p-stopped"), hostRun(6, "cmdr-p-young"),
		{ID: 7, Status: "running", ContainerID: sql.NullString{String: "c1", Valid: true}},
	}
	runs[5].SpawnTime = 1700000000 - 10
	r := newHostReaper(fs, &tmuxctl.MockClient{}, runs, []TmuxSession{
		{Name: "cmdr-p-working", Created: old}, {Name: "cmdr-p-shell", Created: old, Idle: true}, {Name: "cmdr-p-young", Created: old, Idle: true},
	})
	for _, dir := range []string{ExitMarkerDir(), filepath.Dir(StopMarker("x"))} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{ExitMarker("cmdr-p-marked"), ExitMarker("cmdr-p-stopped"), StopMarker("cmdr-p-stopped")} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.FinishedRunningRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != 2 || got[1].ID != 3 {
		t.Fatalf("finished: %+v, want the idle session and the marked one", got)
	}
	if len(fs.closed) != 0 {
		t.Fatalf("closed runs: %v", fs.closed)
	}
	if _, err := os.Stat(ExitMarker("cmdr-p-marked")); err != nil {
		t.Fatalf("exit marker taken from the sweep: %v", err)
	}
}

// An observer never deletes a marker, an earlier run's stale one included; only
// the sweep clears it.
func TestFinishedRunningRunsLeaveAStaleStopMarker(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	run := hostRun(1, "cmdr-p-shell")
	r := newHostReaper(newFakeStore(), &tmuxctl.MockClient{}, []store.AgentRun{run}, []TmuxSession{
		{Name: "cmdr-p-shell", Created: time.Unix(run.SpawnTime, 0), Idle: true},
	})
	if err := os.MkdirAll(StopMarkerDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := StopMarker("cmdr-p-shell")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := time.Unix(run.SpawnTime, 0).Add(-time.Hour)
	if err := os.Chtimes(marker, stale, stale); err != nil {
		t.Fatal(err)
	}
	got, err := r.FinishedRunningRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("a stale marker is no stop: %+v", got)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the observer deleted the marker: %v", err)
	}
	if _, stopped := stoppedBy("cmdr-p-shell", time.Unix(run.SpawnTime, 0)); stopped {
		t.Fatal("stale marker read as a stop")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the sweep's read keeps a stale marker")
	}
}

// A launcher closes its own session after its Operator ends; the exit marker it
// leaves first makes that a finish, not a death. No marker is still a death.
func TestReapHost_GoneSessionWithExitMarkerExited(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	fs := newFakeStore()
	r := newHostReaper(fs, &tmuxctl.MockClient{}, []store.AgentRun{hostRun(1, "cmdr-p-done"), hostRun(2, "cmdr-p-killed")}, nil)
	if err := os.MkdirAll(ExitMarkerDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ExitMarker("cmdr-p-done"), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := r.ReapHost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Exited) != 1 || res.Exited[0].ID != 1 || len(res.Died) != 1 || res.Died[0].ID != 2 {
		t.Fatalf("exited %v, died %v", res.Exited, res.Died)
	}
	if fs.closed[1] != "exited" || fs.closed[2] != "orphan-reaped" {
		t.Errorf("closed: %v", fs.closed)
	}
	if _, err := os.Stat(ExitMarker("cmdr-p-done")); !os.IsNotExist(err) {
		t.Errorf("the marker must be cleared once read: %v", err)
	}
}
