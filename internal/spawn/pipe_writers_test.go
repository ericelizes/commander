package spawn

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/testguard"
)

type pipeWriterStore struct {
	projects []store.Project
	runs     []store.AgentRun
}

func (s pipeWriterStore) ListProjects(context.Context) ([]store.Project, error) {
	return s.projects, nil
}

func (s pipeWriterStore) ListRunningRuns(context.Context) ([]store.AgentRun, error) {
	return s.runs, nil
}

func TestParsePipeWriters(t *testing.T) {
	out := `  101 01-02:03:04 sh -c cat >> /p/.commander/operators/op-a.fifo
  102     05:06 sh -c exec cat >> '/p/.commander/operators/op-b.fifo'
  103     00:01 cat
  104     00:01 sh -c cat >> /tmp/elsewhere.log
`
	got := parsePipeWriters(out)
	want := []PipeWriter{
		{PID: 101, Path: "/p/.commander/operators/op-a.fifo", Age: 26*time.Hour + 3*time.Minute + 4*time.Second},
		{PID: 102, Path: "/p/.commander/operators/op-b.fifo", Age: 5*time.Minute + 6*time.Second},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsePipeWriters = %+v, want %+v", got, want)
	}
}

func TestReapPipeWriters_KillsOnlyUnownedOldWritersOfKnownProjects(t *testing.T) {
	st := pipeWriterStore{
		projects: []store.Project{{ID: 1, Path: "/p"}},
		runs: []store.AgentRun{{
			ProjectID: 1, AgentName: "operator-live", Status: "running",
			TmuxPaneID: sql.NullString{String: "cmdr-p-live", Valid: true},
		}},
	}
	writers := []PipeWriter{
		{PID: 1, Path: "/p/.commander/operators/done.fifo", Age: time.Hour},     // run closed: kill
		{PID: 2, Path: "/p/.commander/operators/live.fifo", Age: time.Hour},     // running Operator
		{PID: 3, Path: "/p/.commander/operators/new.fifo", Age: time.Second},    // spawn in flight
		{PID: 4, Path: "/other/.commander/operators/x.fifo", Age: time.Hour},    // not a project of this store
		{PID: 5, Path: "/p/.commander/operators/done.fifo", Age: 2 * time.Hour}, // old style: its forked child
	}
	var killed []int
	r := pipeWriterReaper{
		store: st,
		list:  func(context.Context) ([]PipeWriter, error) { return writers, nil },
		kill:  func(pid int) error { killed = append(killed, pid); return nil },
	}
	got, err := r.reap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{1, 5}; !reflect.DeepEqual(got, want) || !reflect.DeepEqual(killed, want) {
		t.Errorf("killed %v (returned %v), want %v", killed, got, want)
	}
}

// A real writer blocked opening a fifo nobody reads (what every finished
// Operator used to leave behind) is found and killed; the one a running
// Operator owns is not.
func TestReapPipeWriters_KillsRealBlockedWriter(t *testing.T) {
	project := t.TempDir()
	testguard.NoLeakedProcesses(t, project)
	ops := filepath.Join(project, ".commander", "operators")
	if err := os.MkdirAll(ops, 0o755); err != nil {
		t.Fatal(err)
	}
	start := func(name string) *exec.Cmd {
		fifo := filepath.Join(ops, name+".fifo")
		if err := syscall.Mkfifo(fifo, 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", "-c", "exec cat >> '"+fifo+"'")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd
	}
	done, live := start("done"), start("live")
	st := pipeWriterStore{
		projects: []store.Project{{ID: 1, Path: project}},
		runs: []store.AgentRun{{
			ProjectID: 1, AgentName: "operator-live", Status: "running",
			TmuxPaneID: sql.NullString{String: "cmdr-p-live", Valid: true},
		}},
	}
	r := pipeWriterReaper{
		store: st,
		list: func(ctx context.Context) ([]PipeWriter, error) {
			ws, err := listPipeWriters(ctx)
			for i := range ws {
				ws[i].Age = time.Hour // past the spawn grace
			}
			return ws, err
		},
		kill: killProcess,
	}
	var got []int
	waitFor(t, 3*time.Second, func() bool {
		var err error
		if got, err = r.reap(context.Background()); err != nil {
			t.Fatal(err)
		}
		return len(got) > 0
	})
	if len(got) != 1 || got[0] != done.Process.Pid {
		t.Fatalf("killed %v, want [%d]", got, done.Process.Pid)
	}
	_ = done.Wait()
	if err := live.Process.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("the running Operator's writer was killed: %v", err)
	}
}
