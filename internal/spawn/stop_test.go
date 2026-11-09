package spawn

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/testguard"
	"github.com/example-user/commander/internal/tmuxctl"
	"github.com/example-user/commander/internal/worktreelock"
)

// A stop ends the session and leaves a marker, so the next sweep settles the
// run as stopped: never as died (which restarts it), even when the launcher
// managed to write its exit marker on the way down.
func TestStopHostRun_SweepSettlesItAsStopped(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	run := hostRun(1, "cmdr-p-op")
	var exists = true
	tx := &tmuxctl.MockClient{
		SessionExistsFunc: func(context.Context, string) (bool, error) { return exists, nil },
		KillPaneFunc:      func(context.Context, string) error { exists = false; return nil },
	}
	if err := StopHostRun(context.Background(), tx, run, "manager-ws"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(tx.Calls, "\n"), "KillPane(target==cmdr-p-op:)") {
		t.Fatalf("calls: %v", tx.Calls)
	}
	if err := os.MkdirAll(ExitMarkerDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ExitMarker("cmdr-p-op"), []byte("129\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := newFakeStore()
	r := newHostReaper(fs, &tmuxctl.MockClient{}, []store.AgentRun{run}, nil)
	res, err := r.ReapHost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stopped) != 1 || res.Stopped[0].Run.ID != 1 || res.Stopped[0].By != "manager-ws" || len(res.Died) != 0 || len(res.Exited) != 0 {
		t.Fatalf("result: %+v", res)
	}
	if fs.closed[1] != "stopped" || len(res.ClosedRuns) != 1 {
		t.Errorf("closed: %v / %v", fs.closed, res.ClosedRuns)
	}
	for _, marker := range []string{StopMarker("cmdr-p-op"), ExitMarker("cmdr-p-op")} {
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Errorf("%s must be cleared: %v", marker, err)
		}
	}
}

// A session that is already gone was not stopped by this call: no marker, so
// the sweep still settles what really happened to it.
func TestStopHostRun_GoneSessionIsNotAStop(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	err := StopHostRun(context.Background(), &tmuxctl.MockClient{}, hostRun(1, "cmdr-p-op"), "manager-ws")
	if err == nil || !strings.Contains(err.Error(), "already ended") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(StopMarker("cmdr-p-op")); !os.IsNotExist(err) {
		t.Fatalf("marker written: %v", err)
	}
}

// A bare tmux target matches by prefix: a stop sent once op-x-1's session has
// ended must not end op-x-11's (cmdrd would restart that one as crashed). On a
// private server (explicit -S socket, TMUX unset): only the exact session goes.
func TestStopHostRun_EndsOnlyTheExactSession(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux")
	}
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv("TMUX", "")
	os.Unsetenv("TMUX")
	short, err := os.MkdirTemp("/tmp", "stop.")
	if err != nil {
		t.Fatal(err)
	}
	testguard.NoLeakedProcesses(t, short)
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	ctx := context.Background()
	tx := tmuxctl.NewOnSocket(filepath.Join(short, "s"))
	t.Cleanup(func() {
		for _, s := range []string{"op-x-1", "op-x-11"} {
			_ = tx.KillPane(ctx, tmuxctl.ExactPane(s))
		}
	})
	// The marker path names the socket dir, so NoLeakedProcesses sees the panes.
	if err := tx.NewSessionCommand(ctx, "op-x-11", short, "sh", "-c", "sleep 300", short); err != nil {
		t.Fatal(err)
	}

	err = StopHostRun(ctx, tx, hostRun(1, "op-x-1"), "manager-ws")
	if err == nil || !strings.Contains(err.Error(), "already ended") {
		t.Fatalf("stop of a gone session: err = %v", err)
	}
	if ok, err := tx.SessionExists(ctx, tmuxctl.ExactSession("op-x-11")); err != nil || !ok {
		t.Fatalf("the sibling op-x-11 was ended: %v %v", ok, err)
	}
	if _, err := os.Stat(StopMarker("op-x-1")); !os.IsNotExist(err) {
		t.Fatalf("marker written: %v", err)
	}

	if err := tx.NewSessionCommand(ctx, "op-x-1", short, "sh", "-c", "sleep 300", short); err != nil {
		t.Fatal(err)
	}
	if err := StopHostRun(ctx, tx, hostRun(2, "op-x-1"), "manager-ws"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := tx.SessionExists(ctx, tmuxctl.ExactSession("op-x-1")); ok {
		t.Fatal("op-x-1 still runs")
	}
	if ok, err := tx.SessionExists(ctx, tmuxctl.ExactSession("op-x-11")); err != nil || !ok {
		t.Fatalf("stopping op-x-1 ended op-x-11: %v %v", ok, err)
	}
}

// A kill that fails leaves no marker: the run is still working.
func TestStopHostRun_FailedKillLeavesNoMarker(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	tx := &tmuxctl.MockClient{SessionExistsResult: true, KillPaneErr: os.ErrPermission}
	if err := StopHostRun(context.Background(), tx, hostRun(1, "cmdr-p-op"), "manager-ws"); err == nil {
		t.Fatal("want an error")
	}
	if _, err := os.Stat(StopMarker("cmdr-p-op")); !os.IsNotExist(err) {
		t.Fatalf("marker left: %v", err)
	}
}

// A marker older than the run belongs to an earlier run of the same session
// name: the new run's death is a death.
func TestReapHost_StaleStopMarkerIsIgnored(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	if err := os.MkdirAll(StopMarkerDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(StopMarker("cmdr-p-op"), []byte("manager-ws\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := hostRun(1, "cmdr-p-op")
	run.SpawnTime = time.Now().Add(time.Hour).Unix()
	fs := newFakeStore()
	res, err := newHostReaper(fs, &tmuxctl.MockClient{}, []store.AgentRun{run}, nil).ReapHost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stopped) != 0 || len(res.Died) != 1 {
		t.Fatalf("result: %+v", res)
	}
	if _, err := os.Stat(StopMarker("cmdr-p-op")); !os.IsNotExist(err) {
		t.Fatalf("stale marker kept: %v", err)
	}
}

// runningOnBranch has an Operator running on goodRequest's branch, as an earlier
// process left it (a spawn's lock lives as long as its process).
func runningOnBranch(t *testing.T, dir string) (*Spawner, *fakeStore, store.AgentRun) {
	t.Helper()
	sp, fs, _, _, _, _ := newHostSpawner(t, dir)
	// The live layout: .worktrees links to .worktrees.noindex, so git's path for
	// a respawn under the same name differs from the spawn's and it takes over too.
	if err := os.Mkdir(filepath.Join(dir, ".worktrees.noindex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".worktrees.noindex", filepath.Join(dir, ".worktrees")); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "git", "worktree", "add", "-q", "-b", "feat/auth", filepath.Join(dir, ".worktrees.noindex", "feat-auth"))
	run := store.AgentRun{ProjectID: 42, TaskBeadID: "bd-100", AgentName: "operator-feat-auth", Status: "running", SpawnTime: time.Now().Add(-time.Minute).Unix(),
		TmuxPaneID: sql.NullString{String: hostSessionName("myproj", "feat-auth"), Valid: true}}
	id, err := fs.InsertAgentRun(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	run.ID = id
	return sp, fs, run
}

// stopForSpawn stops that Operator: its marker and running row stand, as they do
// until cmdrd's next sweep.
func stopForSpawn(t *testing.T, dir string) (*Spawner, *fakeStore, store.AgentRun) {
	t.Helper()
	sp, fs, run := runningOnBranch(t, dir)
	tx := &tmuxctl.MockClient{SessionExistsFunc: func(context.Context, string) (bool, error) { return true, nil }}
	if err := StopHostRun(context.Background(), tx, run, "manager-ws"); err != nil {
		t.Fatal(err)
	}
	return sp, fs, run
}

func sameBranchRequest(t *testing.T, dir, name string) SpawnRequest {
	req := goodRequest(t, dir)
	req.TaskBeadID, req.Name = "bd-101", name
	return req
}

// Until the sweep closes its row a stopped run still has its session and row, so
// a spawn on its branch waits; the sweep is all it waits for, under either name.
func TestSpawnAfterStopIsRefusedUntilTheSweep(t *testing.T) {
	for _, name := range []string{"follow-up", ""} {
		t.Run("name="+name, func(t *testing.T) {
			dir := makeGitRepo(t)
			sp, fs, run := stopForSpawn(t, dir)
			req := sameBranchRequest(t, dir, name)

			_, err := sp.Spawn(context.Background(), req)
			if err == nil || !strings.Contains(err.Error(), "next sweep") {
				t.Fatalf("before the sweep: %v", err)
			}
			if len(fs.runs) != 1 {
				t.Fatalf("a refused spawn must start nothing: %v", fs.runs)
			}

			res, err := newHostReaper(fs, &tmuxctl.MockClient{}, []store.AgentRun{run}, nil).ReapHost(context.Background())
			if err != nil || len(res.Stopped) != 1 {
				t.Fatalf("sweep: %+v %v", res, err)
			}
			// No settle ran, and none has to: one that was dropped or died with
			// cmdrd refuses nothing.
			if _, err := sp.Spawn(context.Background(), req); err != nil {
				t.Fatalf("after the sweep: %v", err)
			}
		})
	}
}

// Stop, then spawn, then the settle (late, or a retry): the new Operator is on
// the worktree and the freeze leaves it writable, under either name and through
// the .worktrees link. A real lock, so a freeze would show on the file.
func TestStopThenSpawnThenSettleFreezesNothing(t *testing.T) {
	for _, name := range []string{"follow-up", ""} {
		t.Run("name="+name, func(t *testing.T) {
			dir := makeGitRepo(t)
			sp, fs, run := stopForSpawn(t, dir)
			if _, err := newHostReaper(fs, &tmuxctl.MockClient{}, []store.AgentRun{run}, nil).ReapHost(context.Background()); err != nil {
				t.Fatal(err)
			}
			res, err := sp.Spawn(context.Background(), sameBranchRequest(t, dir, name))
			if err != nil {
				t.Fatalf("spawn: %v", err)
			}
			if err := FreezeWorktree(context.Background(), fs.ListRunningRuns, worktreelock.Lock, dir, run); err != nil {
				t.Fatal(err)
			}
			probe := filepath.Join(res.WorktreePath, "probe")
			if err := os.WriteFile(probe, nil, 0o644); err != nil {
				t.Fatalf("the new Operator's worktree was frozen: %v", err)
			}
		})
	}
}

// A follow-up Operator's row says where it works, so its own settle freezes the
// worktree it took over, not the one its name would give.
func TestSpawnRecordsTheWorktreeItTookOver(t *testing.T) {
	dir := makeGitRepo(t)
	sp, fs, run := runningOnBranch(t, dir)
	if err := fs.MarkClosed(context.Background(), run.ID, "exited"); err != nil {
		t.Fatal(err)
	}
	res, err := sp.Spawn(context.Background(), sameBranchRequest(t, dir, "follow-up"))
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := fs.ListRunningRuns(context.Background())
	if len(rows) != 1 || rows[0].WorktreePath.String != res.WorktreePath || filepath.Base(res.WorktreePath) != "feat-auth" {
		t.Fatalf("rows %+v, result %s", rows, res.WorktreePath)
	}
	var locked []string
	lock := func(d string) error { locked = append(locked, d); return nil }
	if err := FreezeWorktree(context.Background(), func(context.Context) ([]store.AgentRun, error) { return nil, nil }, lock, dir, rows[0]); err != nil {
		t.Fatal(err)
	}
	if len(locked) != 1 || locked[0] != res.WorktreePath {
		t.Fatalf("locked %v, want %s", locked, res.WorktreePath)
	}
}

func freezeCount(t *testing.T, running func(context.Context) ([]store.AgentRun, error), project string, closed store.AgentRun) (int, error) {
	t.Helper()
	n := 0
	err := FreezeWorktree(context.Background(), running, func(string) error { n++; return nil }, project, closed)
	return n, err
}

// An unreadable read freezes nothing and says so.
func TestFreezeWithAnUnreadableReadFreezesNothing(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	n, err := freezeCount(t, func(context.Context) ([]store.AgentRun, error) { return nil, os.ErrPermission }, "/p", store.AgentRun{ID: 1, AgentName: "operator-x"})
	if n != 0 || err == nil {
		t.Fatalf("froze %d, err %v", n, err)
	}
}

// A row from before the column has the worktree of its name. Other projects and
// other worktrees are no owners, and the closed run is not its own.
func TestFreezeOwners(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	closed := store.AgentRun{ID: 1, ProjectID: 3, AgentName: "operator-x"}
	for name, tc := range map[string]struct {
		runs []store.AgentRun
		want int
	}{
		"nobody":        {nil, 1},
		"itself":        {[]store.AgentRun{closed}, 1},
		"same name":     {[]store.AgentRun{{ID: 2, ProjectID: 3, AgentName: "operator-x"}}, 0},
		"took it over":  {[]store.AgentRun{{ID: 2, ProjectID: 3, AgentName: "operator-y", WorktreePath: sql.NullString{String: "/p/.worktrees/x", Valid: true}}}, 0},
		"other tree":    {[]store.AgentRun{{ID: 2, ProjectID: 3, AgentName: "operator-y"}}, 1},
		"other project": {[]store.AgentRun{{ID: 2, ProjectID: 4, AgentName: "operator-x"}}, 1},
	} {
		n, err := freezeCount(t, func(context.Context) ([]store.AgentRun, error) { return tc.runs, nil }, "/p", closed)
		if err != nil || n != tc.want {
			t.Errorf("%s: froze %d, want %d (%v)", name, n, tc.want, err)
		}
	}
}

// The spawn's row comes only after it clears the lock. A freeze in that gap finds
// the claim held and stands down; one already running makes the spawn wait, so its
// clear comes last. The claim goes with its holder, so nothing stays held.
func TestWorktreeClaimKeepsFreezeAndSpawnApart(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	closed := store.AgentRun{ID: 1, AgentName: "operator-x"}
	none := func(context.Context) ([]store.AgentRun, error) { return nil, nil }

	release, err := claimWorktree(context.Background(), "/p/.worktrees/x")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := freezeCount(t, none, "/p", closed); n != 0 || err != nil {
		t.Fatalf("froze %d during a spawn (%v)", n, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := claimWorktree(ctx, "/p/.worktrees/x"); err == nil {
		t.Fatal("a second holder got the claim")
	}
	release()
	if n, err := freezeCount(t, none, "/p", closed); n != 1 || err != nil {
		t.Fatalf("froze %d after the release (%v)", n, err)
	}
	again, err := claimWorktree(context.Background(), "/p/.worktrees/x")
	if err != nil {
		t.Fatalf("the freeze kept the claim: %v", err)
	}
	again()
}

// .worktrees may link to .worktrees.noindex: one worktree, one claim, also before
// it exists.
func TestWorktreeClaimFollowsTheLink(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".worktrees.noindex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".worktrees.noindex", filepath.Join(dir, ".worktrees")); err != nil {
		t.Fatal(err)
	}
	release, err := claimWorktree(context.Background(), filepath.Join(dir, ".worktrees", "x"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, held, err := tryClaimWorktree(filepath.Join(dir, ".worktrees.noindex", "x")); held || err != nil {
		t.Fatalf("held %v, err %v", held, err)
	}
}

// A marker that cannot be written ends nothing, so no stop stands to be refused
// and the session is not read as crashed.
func TestStopWithAFailedMarkerWriteStopsNothing(t *testing.T) {
	dir := makeGitRepo(t)
	sp, fs, run := runningOnBranch(t, dir)
	if err := os.WriteFile(StopMarkerDir(), nil, 0o600); err != nil { // a file where the directory goes
		t.Fatal(err)
	}
	killed := 0
	tx := &tmuxctl.MockClient{
		SessionExistsFunc: func(context.Context, string) (bool, error) { return true, nil },
		KillPaneFunc:      func(context.Context, string) error { killed++; return nil },
	}
	if err := StopHostRun(context.Background(), tx, run, "manager-ws"); err == nil {
		t.Fatal("want the marker error")
	}
	if killed != 0 {
		t.Fatal("killed the pane with no marker: the sweep would read a crash")
	}
	if err := fs.MarkClosed(context.Background(), run.ID, "exited"); err != nil {
		t.Fatal(err)
	}
	if _, err := sp.Spawn(context.Background(), sameBranchRequest(t, dir, "follow-up")); err != nil {
		t.Fatalf("spawn: %v", err)
	}
}

// The sweep could not remove the marker (read-only directory, a crash right after
// the close): with the row closed it refuses nothing, however long it stays.
func TestAMarkerLeftByASweepRefusesNothing(t *testing.T) {
	dir := makeGitRepo(t)
	sp, fs, run := stopForSpawn(t, dir)
	if err := os.Chmod(StopMarkerDir(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(StopMarkerDir(), 0o755) })
	if _, err := newHostReaper(fs, &tmuxctl.MockClient{}, []store.AgentRun{run}, nil).ReapHost(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(StopMarker(run.TmuxPaneID.String)); err != nil {
		t.Fatalf("the marker should have stayed: %v", err)
	}
	if _, err := sp.Spawn(context.Background(), sameBranchRequest(t, dir, "")); err != nil {
		t.Fatalf("spawn: %v", err)
	}
}

// Only a sweep clears a marker, and it reads running rows only: a run closed
// another way (a ceiling kill) must not hold its branch forever.
func TestSpawnAfterStopIgnoresAMarkerWhoseRunClosedAnotherWay(t *testing.T) {
	dir := makeGitRepo(t)
	sp, fs, run := stopForSpawn(t, dir)
	if err := fs.MarkClosed(context.Background(), run.ID, "killed-ceiling"); err != nil {
		t.Fatal(err)
	}
	if _, err := sp.Spawn(context.Background(), sameBranchRequest(t, dir, "follow-up")); err != nil {
		t.Fatalf("spawn: %v", err)
	}
}

// A marker older than the running row is an earlier run's, as in the sweep.
func TestSpawnAfterStopIgnoresAnEarlierRunsMarker(t *testing.T) {
	dir := makeGitRepo(t)
	sp, _, run := runningOnBranch(t, dir)
	session := run.TmuxPaneID.String
	if err := os.MkdirAll(StopMarkerDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(StopMarker(session), []byte("manager-ws\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(StopMarker(session), old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := sp.Spawn(context.Background(), sameBranchRequest(t, dir, "")); err != nil {
		t.Fatalf("spawn: %v", err)
	}
}

// On a plain .worktrees directory git's path equals the spawn's, so a respawn
// under the old name is no takeover; it is refused the same.
func TestSpawnAfterStopRefusesTheSamePathOnAPlainWorktreesDir(t *testing.T) {
	dir := makeGitRepo(t)
	sp, fs, _, _, _, _ := newHostSpawner(t, dir)
	wt := filepath.Join(dir, ".worktrees", "feat-auth")
	mustRun(t, dir, "git", "worktree", "add", "-q", "-b", "feat/auth", wt)
	session := hostSessionName("myproj", "feat-auth")
	run := store.AgentRun{ProjectID: 42, TaskBeadID: "bd-100", AgentName: "operator-feat-auth", Status: "running", SpawnTime: time.Now().Add(-time.Minute).Unix(),
		TmuxPaneID: sql.NullString{String: session, Valid: true}}
	if _, err := fs.InsertAgentRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	tx := &tmuxctl.MockClient{SessionExistsFunc: func(context.Context, string) (bool, error) { return true, nil }}
	if err := StopHostRun(context.Background(), tx, run, "manager-ws"); err != nil {
		t.Fatal(err)
	}
	_, err := sp.Spawn(context.Background(), sameBranchRequest(t, dir, ""))
	if err == nil || !strings.Contains(err.Error(), "next sweep") {
		t.Fatalf("spawn: %v", err)
	}
}

// fileModes maps every path under root to its mode, links not followed.
func fileModes(t *testing.T, root string) map[string]os.FileMode {
	t.Helper()
	modes := map[string]os.FileMode{}
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		modes[path] = info.Mode()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return modes
}

// The lock is a recursive chmod, so a row naming anything but an Operator worktree
// must change no mode: not the main checkout with every worktree under it, not a
// path nested in a worktree, not a link leading out. Real git and a real lock.
func TestFreezeRefusesWhatIsNoOperatorWorktree(t *testing.T) {
	for name, path := range map[string]func(dir, outside string) string{
		"main checkout": func(dir, _ string) string { return dir },
		"folder itself": func(dir, _ string) string { return filepath.Join(dir, ".worktrees") },
		"nested":        func(dir, _ string) string { return filepath.Join(dir, ".worktrees", "sibling", "nested") },
		"link out":      func(dir, _ string) string { return filepath.Join(dir, ".worktrees", "out") },
		"climbs out":    func(dir, _ string) string { return dir + "/.worktrees/sibling/../.." },
		"elsewhere":     func(_, outside string) string { return outside },
	} {
		t.Run(name, func(t *testing.T) {
			dir, outside := makeGitRepo(t), t.TempDir()
			t.Setenv("COMMANDER_HOME", t.TempDir())
			mustRun(t, dir, "git", "worktree", "add", "-q", "-b", "op/sibling", filepath.Join(dir, ".worktrees", "sibling"))
			if err := os.Mkdir(filepath.Join(dir, ".worktrees", "sibling", "nested"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(outside, "file"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(dir, ".worktrees", "out")); err != nil {
				t.Fatal(err)
			}
			before, beforeOut := fileModes(t, dir), fileModes(t, outside)
			run := store.AgentRun{ID: 1, AgentName: "operator-x", WorktreePath: sql.NullString{String: path(dir, outside), Valid: true}}
			none := func(context.Context) ([]store.AgentRun, error) { return nil, nil }
			if err := FreezeWorktree(context.Background(), none, worktreelock.Lock, dir, run); err != nil {
				t.Fatal(err)
			}
			if after := fileModes(t, dir); !reflect.DeepEqual(before, after) {
				t.Fatalf("modes under the project changed: %v -> %v", before, after)
			}
			if after := fileModes(t, outside); !reflect.DeepEqual(beforeOut, after) {
				t.Fatalf("modes outside changed: %v -> %v", beforeOut, after)
			}
		})
	}
}

// The check lets a real worktree through, under either folder and through the
// link between them: the guard must not end freezing.
func TestFreezeStillLocksAnOperatorWorktree(t *testing.T) {
	for _, recorded := range []string{".worktrees", ".worktrees.noindex"} {
		t.Run(recorded, func(t *testing.T) {
			dir := makeGitRepo(t)
			if err := os.Mkdir(filepath.Join(dir, ".worktrees.noindex"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(".worktrees.noindex", filepath.Join(dir, ".worktrees")); err != nil {
				t.Fatal(err)
			}
			t.Setenv("COMMANDER_HOME", t.TempDir())
			wt := filepath.Join(dir, recorded, "own")
			mustRun(t, dir, "git", "worktree", "add", "-q", "-b", "op/own", filepath.Join(dir, ".worktrees.noindex", "own"))
			t.Cleanup(func() { _ = worktreelock.Unlock(wt) })
			run := store.AgentRun{ID: 1, AgentName: "operator-x", WorktreePath: sql.NullString{String: wt, Valid: true}}
			none := func(context.Context) ([]store.AgentRun, error) { return nil, nil }
			if err := FreezeWorktree(context.Background(), none, worktreelock.Lock, dir, run); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(wt, "probe"), nil, 0o644); err == nil {
				t.Fatal("the worktree was not frozen")
			}
		})
	}
}

// A branch the main checkout has, or any checkout outside the worktree folders, is
// never taken over: nothing starts there and nothing changes on disk.
func TestSpawnOnABranchCheckedOutOutsideIsRefused(t *testing.T) {
	dir := makeGitRepo(t)
	sp, fs, _, _, _, _ := newHostSpawner(t, dir)
	mustRun(t, dir, "git", "worktree", "add", "-q", "-b", "op/away", filepath.Join(t.TempDir(), "away"))
	current, err := exec.Command("git", "-C", dir, "branch", "--show-current").Output()
	if err != nil {
		t.Fatal(err)
	}

	for name, branch := range map[string]string{
		"main checkout's branch": strings.TrimSpace(string(current)),
		"a checkout elsewhere":   "op/away",
	} {
		t.Run(name, func(t *testing.T) {
			before := fileModes(t, dir)
			req := goodRequest(t, dir)
			req.Branch = branch
			_, err := sp.Spawn(context.Background(), req)
			if err == nil || !strings.Contains(err.Error(), "outside the project's worktree folders") || !strings.Contains(err.Error(), branch) {
				t.Fatalf("spawn: %v", err)
			}
			if len(fs.runs) != 0 {
				t.Fatalf("a refused spawn must start nothing: %v", fs.runs)
			}
			after := fileModes(t, dir)
			for path, mode := range before {
				if after[path] != mode {
					t.Fatalf("%s: mode %v -> %v", path, mode, after[path])
				}
			}
		})
	}
}
