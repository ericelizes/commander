package ceilings

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/example-user/commander/internal/store"
)

// runState is the watchdog's per-operator scratchpad. Lives in
// Watchdog.runs (sync.Map) keyed by runID. Every consumer goroutine
// updates ITS OWN runState; cross-run reads (RunUsage, tickLoop) take the
// per-state mutex.
//
// The mutex is held only across cheap field reads/writes — no I/O. Kill
// paths happen outside the mutex via killOnce.
type runState struct {
	runID       int64
	containerID string // populated for Docker code operators
	tmuxPaneID  string // tmux session of a host operator (knowledge-type, or host-runtime code)
	sessionID   string
	projectID   int64
	projectName string
	projectPath string
	taskBeadID  string
	agentName   string

	// isKnowledge is true for every host operator (a tmux session, no
	// container): it selects the tmux fifo over docker logs for the event
	// source, and tmux kill-pane over docker stop for the kill.
	isKnowledge bool

	spawnTime time.Time

	mu                  sync.Mutex
	tokensUsed          int64
	usdUsed             float64
	consecutiveFailures int

	// killOnce gates the kill path so concurrent breaches (e.g. token
	// ceiling and USD ceiling on the same step_finish) don't double-stop
	// the container or insert two cost_events rows.
	killOnce sync.Once
}

// newRunState builds runState from a store.AgentRun. The caller (Register)
// fills the projectName + projectPath separately when known — they are not
// stored on AgentRun directly. Default zero values are fine for the watchdog
// loop; kill paths log a degraded message when projectName is empty.
func newRunState(r store.AgentRun) *runState {
	st := &runState{
		runID:               r.ID,
		sessionID:           r.OpencodeSessionID,
		projectID:           r.ProjectID,
		taskBeadID:          r.TaskBeadID,
		agentName:           r.AgentName,
		spawnTime:           time.Unix(r.SpawnTime, 0),
		tokensUsed:          r.TokensUsed,
		usdUsed:             r.UsdUsed,
		consecutiveFailures: int(r.ConsecutiveFailures),
	}
	if r.ContainerID.Valid {
		st.containerID = r.ContainerID.String
	}
	if r.TmuxPaneID.Valid {
		st.tmuxPaneID = r.TmuxPaneID.String
		st.isKnowledge = true
	}
	return st
}

// fifoPath is where a host operator's nd-JSON is piped by tmux pipe-pane:
// <project>/.commander/operators/<flat>.fifo, where the agent is
// operator-<flat> (see internal/spawn). Rows that stored an absolute path in
// tmuxPaneID are honored as-is. Empty when it cannot be derived.
func (st *runState) fifoPath() string {
	if filepath.IsAbs(st.tmuxPaneID) {
		return st.tmuxPaneID
	}
	st.mu.Lock()
	projectPath := st.projectPath
	st.mu.Unlock()
	if projectPath == "" || st.agentName == "" {
		return ""
	}
	flat := strings.TrimPrefix(st.agentName, "operator-")
	return filepath.Join(projectPath, ".commander", "operators", flat+".fifo")
}

// SetProjectMeta sets the project-name + project-path fields the kill path
// needs to format am subjects + invoke `bd -C <path> set-state`. The watchdog
// is unable to derive these from store.AgentRun alone (project_id is just an
// integer); callers (spawn glue) thread them through.
func (st *runState) SetProjectMeta(name, path string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.projectName = name
	st.projectPath = path
}
