package ceilings

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/example-user/commander/internal/store"
)

// hostRunState is the watchdog's view of a host-runtime code operator: a tmux
// session id and no container id.
func hostRunState(projectPath string) *runState {
	st := newRunState(store.AgentRun{
		ID:         11,
		ProjectID:  5,
		TaskBeadID: "bd-11",
		AgentName:  "operator-op-fix-bd-11",
		SpawnTime:  time.Now().Unix(),
		TmuxPaneID: sql.NullString{String: "cmdr-proj-op-fix-bd-11", Valid: true},
	})
	st.SetProjectMeta("proj", projectPath)
	return st
}

func TestNewRunState_HostOperatorHasSessionNotContainer(t *testing.T) {
	st := hostRunState("/p")
	if !st.isKnowledge || st.containerID != "" || st.tmuxPaneID != "cmdr-proj-op-fix-bd-11" {
		t.Fatalf("host run state: %+v", st)
	}
}

func TestKill_HostOperatorKillsTmuxPaneNotDocker(t *testing.T) {
	w, fs, dm, bm, am := newKillHarness(t)
	var mu sync.Mutex
	var killed []string
	w.killPane = func(_ context.Context, target string) error {
		mu.Lock()
		defer mu.Unlock()
		killed = append(killed, target)
		return nil
	}

	st := hostRunState("/abs/proj")
	w.runs.Store(st.runID, st)
	w.kill(context.Background(), st, "token ceiling: 9 >= 8")

	if len(killed) != 1 || killed[0] != "cmdr-proj-op-fix-bd-11" {
		t.Fatalf("killPane targets = %v", killed)
	}
	for _, c := range dm.Calls {
		if strings.HasPrefix(c, "Stop(") {
			t.Fatalf("a host operator has no container to stop: %v", dm.Calls)
		}
	}
	// The rest of the chain still runs: lead notified, bead blocked, run closed.
	if len(am.Calls) != 1 || len(bm.Calls) != 1 || !strings.Contains(bm.Calls[0], "blocked") {
		t.Errorf("am=%v beads=%v", am.Calls, bm.Calls)
	}
	if fs.callsCount("MarkClosed:killed-ceiling") != 1 || fs.callsCount("InsertCostEvent:ceiling-hit") != 1 {
		t.Errorf("run must be closed and recorded")
	}
}

func TestKill_HostOperatorKillFailureStillClosesTheRun(t *testing.T) {
	w, fs, _, _, _ := newKillHarness(t)
	w.killPane = func(context.Context, string) error { return os.ErrNotExist }

	st := hostRunState("/abs/proj")
	w.runs.Store(st.runID, st)
	w.kill(context.Background(), st, "retry ceiling")

	if fs.callsCount("MarkClosed:killed-ceiling") != 1 {
		t.Errorf("a failed kill-pane must not leave the run open")
	}
}

func TestKill_LegacyFifoPathInPaneIdIsNotKilledAsATarget(t *testing.T) {
	w, _, _, _, _ := newKillHarness(t)
	called := false
	w.killPane = func(context.Context, string) error { called = true; return nil }

	st := hostRunState("/abs/proj")
	st.tmuxPaneID = "/abs/proj/.commander/operators/x.fifo"
	w.runs.Store(st.runID, st)
	w.kill(context.Background(), st, "x")

	if called {
		t.Error("an absolute path is not a tmux target")
	}
}

func TestFifoPath(t *testing.T) {
	st := hostRunState("/abs/proj")
	if got, want := st.fifoPath(), "/abs/proj/.commander/operators/op-fix-bd-11.fifo"; got != want {
		t.Errorf("fifoPath = %q, want %q", got, want)
	}

	legacy := hostRunState("/abs/proj")
	legacy.tmuxPaneID = "/some/where/a.fifo"
	if got := legacy.fifoPath(); got != "/some/where/a.fifo" {
		t.Errorf("an absolute pane id is the fifo path itself, got %q", got)
	}

	if got := hostRunState("").fifoPath(); got != "" {
		t.Errorf("without a project path nothing can be derived, got %q", got)
	}
}

func TestDockerLogsSource_HostOperatorTailsItsFifo(t *testing.T) {
	if _, err := exec.LookPath("tail"); err != nil {
		t.Skip("tail not available")
	}
	project := t.TempDir()
	dir := filepath.Join(project, ".commander", "operators")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A regular file stands in for the fifo: tail -F reads both the same way.
	if err := os.WriteFile(filepath.Join(dir, "op-fix-bd-11.fifo"), []byte("{\"type\":\"x\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	lr, err := dockerLogsSource{}.Open(ctx, hostRunState(project))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer lr.Close()
	if !lr.Scan() || string(lr.Bytes()) != `{"type":"x"}` {
		t.Fatalf("expected the fifo line, got %q (err %v)", lr.Bytes(), lr.Err())
	}

	if _, err := (dockerLogsSource{}).Open(ctx, hostRunState("")); err == nil {
		t.Error("a host run with no derivable fifo must error")
	}
}
