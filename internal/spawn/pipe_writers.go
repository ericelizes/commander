package spawn

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/example-user/commander/internal/store"
)

// pipeWriterRE matches the tmux pipe-pane writer of a host Operator's output,
// old form (`cat >> <path>`) and current (`exec cat >> '<path>'`). Only a
// writer still blocked opening a fifo shows its path; one that reached cat is
// plain `cat`, and ends with its pane.
var pipeWriterRE = regexp.MustCompile(`^(?:/bin/)?sh -c (?:exec )?cat >> '?(/.+/\.commander/operators/[^/']+\.fifo)'?$`)

// PipeWriter is one pipe-pane writer process.
type PipeWriter struct {
	PID  int
	Path string
	Age  time.Duration
}

// PipeWriterStore is the slice of *store.Store the pipe writer reaper needs.
type PipeWriterStore interface {
	ListProjects(ctx context.Context) ([]store.Project, error)
	ListRunningRuns(ctx context.Context) ([]store.AgentRun, error)
}

// pipeWriterReaper kills writers that outlived their Operator. Host Operators
// now pipe to a regular file, so this is the safety net for writers started
// on a fifo by older builds, and for any that block some other way.
type pipeWriterReaper struct {
	store PipeWriterStore
	list  func(ctx context.Context) ([]PipeWriter, error)
	kill  func(pid int) error
}

// ReapPipeWriters kills every pipe-pane writer of a registered project's
// Operator output that no running agent_runs row owns and that is older than
// hostStartGrace (a spawn in flight wires the pipe before it inserts the row).
// It returns the killed pids.
func ReapPipeWriters(ctx context.Context, st PipeWriterStore) ([]int, error) {
	return pipeWriterReaper{store: st, list: listPipeWriters, kill: killProcess}.reap(ctx)
}

func (r pipeWriterReaper) reap(ctx context.Context) ([]int, error) {
	projects, err := r.store.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("reaper: list projects: %w", err)
	}
	runs, err := r.store.ListRunningRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("reaper: list running runs: %w", err)
	}
	writers, err := r.list(ctx)
	if err != nil {
		return nil, fmt.Errorf("reaper: list pipe writers: %w", err)
	}
	pathOf := make(map[int64]string, len(projects))
	var operatorDirs []string
	for _, p := range projects {
		pathOf[p.ID] = p.Path
		operatorDirs = append(operatorDirs, filepath.Join(p.Path, ".commander", "operators")+"/")
	}
	owned := map[string]bool{}
	for _, run := range runs {
		if run.TmuxPaneID.Valid && run.TmuxPaneID.String != "" {
			owned[runOutputPath(run, pathOf[run.ProjectID])] = true
		}
	}
	var killed []int
	var firstErr error
	for _, w := range writers {
		if owned[w.Path] || w.Age < hostStartGrace || !underAny(w.Path, operatorDirs) {
			continue
		}
		if err := r.kill(w.PID); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("reaper: kill pipe writer %d: %w", w.PID, err)
			}
			continue
		}
		killed = append(killed, w.PID)
	}
	return killed, firstErr
}

// runOutputPath is where a host run's pane output goes, the same derivation
// as the ceilings watchdog (runState.fifoPath).
func runOutputPath(run store.AgentRun, projectPath string) string {
	if filepath.IsAbs(run.TmuxPaneID.String) {
		return run.TmuxPaneID.String
	}
	flat := strings.TrimPrefix(run.AgentName, "operator-")
	return filepath.Join(projectPath, ".commander", "operators", flat+".fifo")
}

func underAny(path string, dirs []string) bool {
	for _, d := range dirs {
		if strings.HasPrefix(path, d) {
			return true
		}
	}
	return false
}

// listPipeWriters reads pipe-pane writers from ps.
func listPipeWriters(ctx context.Context) ([]PipeWriter, error) {
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,etime=,command=").Output()
	if err != nil {
		return nil, err
	}
	return parsePipeWriters(string(out)), nil
}

// parsePipeWriters picks the pipe writers out of "pid etime command" lines.
func parsePipeWriters(out string) []PipeWriter {
	var writers []PipeWriter
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		m := pipeWriterRE.FindStringSubmatch(strings.Join(f[2:], " "))
		if m == nil {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		age, ok := parseEtime(f[1])
		if err != nil || !ok {
			continue
		}
		writers = append(writers, PipeWriter{PID: pid, Path: m[1], Age: age})
	}
	return writers
}

// parseEtime reads ps's elapsed time, [[dd-]hh:]mm:ss.
func parseEtime(s string) (time.Duration, bool) {
	var days int
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, false
		}
		days, s = n, rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	secs := 0
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, false
		}
		secs = secs*60 + n
	}
	return time.Duration(days)*24*time.Hour + time.Duration(secs)*time.Second, true
}

func killProcess(pid int) error {
	err := syscall.Kill(pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}
