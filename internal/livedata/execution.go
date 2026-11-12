package livedata

import (
	"fmt"
	"slices"
	"strings"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// SnapExecution is the one observed run set used for presence, row counts and
// fleet counts. Assignment and process presence do not imply an active turn.
type SnapExecution struct {
	StartupAttempts int64  `json:"startup_attempts,omitempty"`
	StartupError    string `json:"startup_error,omitempty"`
	Role            string `json:"role"`
	State           string `json:"state"`
	ProjectID       int64  `json:"project_id"`
	BeadID          string `json:"bead_id"`
	TaskBeadID      string `json:"task_bead_id,omitempty"`
	RunID           int64  `json:"run_id,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	Runtime         string `json:"runtime,omitempty"` // claude or codex
}

func executionOf(s Snapshot, data Data, chats map[string]ChatState) []SnapExecution {
	if data.Postgres && data.ObservedExecutions != nil {
		return observedExecutionOf(s, data)
	}

	out := map[string]SnapExecution{}
	alive := map[string]bool{}
	if s.Live != nil {
		for _, role := range s.Live.Operators {
			alive[beads.OwnerLabelName(role)] = true
		}
	}

	type scopeKey struct {
		pid int64
		id  string
	}
	visible := map[scopeKey]bool{}
	owners := map[scopeKey]string{}
	for _, ws := range s.Workstreams {
		visible[scopeKey{ws.ProjectID, ws.BeadID}] = true
		var collect func([]SnapStep)
		collect = func(ss []SnapStep) {
			for _, step := range ss {
				visible[scopeKey{ws.ProjectID, step.BeadID}] = true
				collect(step.Children)
			}
		}
		collect(ws.Steps)
	}
	for _, o := range data.Objectives {
		if visible[scopeKey{o.ProjectID, o.BeadID}] && o.Owner != "" {
			owners[scopeKey{o.ProjectID, beads.OwnerLabelName(o.Owner)}] = o.BeadID
		}
	}
	states := map[string]SnapAgent{}
	for role, a := range s.Agents {
		k := beads.OwnerLabelName(role)
		old, ok := states[k]
		if !ok || a.Since > old.Since || role == k {
			states[k] = a
		}
	}
	for _, op := range data.Operators {
		role := beads.OwnerLabelName(op.AgentName)
		if !alive[role] {
			continue
		}
		state := "unknown"
		if a, ok := states[role]; ok {
			state = a.State
		}
		scope := op.TaskBeadID
		if !visible[scopeKey{op.ProjectID, scope}] {
			if owner := owners[scopeKey{op.ProjectID, role}]; owner != "" {
				scope = owner
			}
		}
		out[role] = SnapExecution{Role: role, State: state, ProjectID: op.ProjectID, BeadID: scope, TaskBeadID: op.TaskBeadID, RunID: op.RunID, SessionID: op.SessionID}
	}
	for role, c := range chats {
		key := role
		if strings.HasPrefix(role, "operator-") {
			key = beads.OwnerLabelName(role)
		}
		x := out[key]
		x.Role, x.State = key, c.State
		if strings.HasPrefix(role, "manager-") {
			id := strings.TrimPrefix(role, "manager-")
			for _, o := range data.Objectives {
				if o.BeadID == id {
					x.ProjectID, x.BeadID = o.ProjectID, o.BeadID
					break
				}
			}
		}
		out[key] = x
	}
	result := make([]SnapExecution, 0, len(out))
	for _, x := range out {
		result = append(result, x)
	}
	slices.SortFunc(result, func(a, b SnapExecution) int { return strings.Compare(a.Role, b.Role) })
	return result
}

func executionCount(s Snapshot, pid int64, id string, steps []SnapStep) int {
	ids := map[string]bool{id: true}
	var walk func([]SnapStep)
	walk = func(steps []SnapStep) {
		for _, st := range steps {
			ids[st.BeadID] = true
			walk(st.Children)
		}
	}
	walk(steps)
	roles := map[string]bool{}
	for _, run := range s.Executions {
		if run.ProjectID == pid && ids[run.BeadID] && run.State == store.AgentWorking {
			key := run.Role
			if run.RunID > 0 {
				key = fmt.Sprintf("run:%d", run.RunID)
			}
			roles[key] = true
		}
	}
	return len(roles)
}

// ownRuntimes are the runtimes (claude, codex; "agent" when unknown) of the
// agents working on a row's own steps: not those inside its nested
// workstreams, which show on their own rows.
func ownRuntimes(s Snapshot, pid int64, id string, steps []SnapStep) []string {
	ids := map[string]bool{id: true}
	var walk func([]SnapStep)
	walk = func(steps []SnapStep) {
		for _, st := range steps {
			if st.Track {
				continue
			}
			ids[st.BeadID] = true
			walk(st.Children)
		}
	}
	walk(steps)
	seen := map[string]bool{}
	var out []string
	for _, run := range s.Executions {
		if run.ProjectID != pid || !ids[run.BeadID] || run.State != store.AgentWorking {
			continue
		}
		key := run.Role
		if run.RunID > 0 {
			key = fmt.Sprintf("run:%d", run.RunID)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		rt := run.Runtime
		if rt == "" {
			rt = "agent"
		}
		out = append(out, rt)
	}
	return out
}

func applyExecutionCounts(s *Snapshot, d *Display) {
	if s.Executions == nil {
		return
	}
	s.Fleet.Agents = len(s.Executions)
	s.Fleet.Active = 0
	s.Fleet.Blocked = 0
	for _, run := range s.Executions {
		if run.State == store.AgentWorking {
			s.Fleet.Active++
		}
		if run.State == store.AgentStuck {
			s.Fleet.Blocked++
		}
	}
	if s.Fleet.Agents > 0 {
		s.Fleet.IdleRatio = float64(s.Fleet.Agents-s.Fleet.Active-s.Fleet.Blocked) / float64(s.Fleet.Agents)
	} else {
		s.Fleet.IdleRatio = 0
	}
	for i := range d.Workstreams {
		r := &d.Workstreams[i]
		var ws *SnapWorkstream
		for j := range s.Workstreams {
			if s.Workstreams[j].ProjectID == projectIDFor(s, r.Project) && s.Workstreams[j].BeadID == r.BeadID {
				ws = &s.Workstreams[j]
				break
			}
		}
		if ws == nil {
			continue
		}
		r.Working = executionCount(*s, ws.ProjectID, ws.BeadID, ws.Steps)
		r.AgentRuntimes = ownRuntimes(*s, ws.ProjectID, ws.BeadID, ws.Steps)
		// A next-actor decision already weighs executions and Commander's own work.
		if s.Storage == "postgres" && r.Working == 0 && r.Kind == "working" && r.Actor == nil {
			r.Kind = "waiting"
			r.Text = waitingExecutionText(*s, ws.ProjectID, ws.BeadID, ws.Steps)
		}

		updateTrackExecutions(*s, ws.ProjectID, ws.Steps, r.Tracks)
	}
}
func projectIDFor(s *Snapshot, name string) int64 {
	for _, p := range s.Projects {
		if p.Name == name {
			return p.ID
		}
	}
	return 0
}
func updateTrackExecutions(s Snapshot, pid int64, steps []SnapStep, tracks []BoardTrack) {
	byID := map[string]SnapStep{}
	var walk func([]SnapStep)
	walk = func(ss []SnapStep) {
		for _, st := range ss {
			byID[st.BeadID] = st
			walk(st.Children)
		}
	}
	walk(steps)
	for i := range tracks {
		st := byID[tracks[i].BeadID]
		tracks[i].Working = executionCount(s, pid, tracks[i].BeadID, st.Children)
		tracks[i].AgentRuntimes = ownRuntimes(s, pid, tracks[i].BeadID, st.Children)
		if s.Storage == "postgres" && tracks[i].Working == 0 && tracks[i].Kind == "working" && tracks[i].Actor == nil {
			tracks[i].Kind = "waiting"
			tracks[i].Text = waitingExecutionText(s, pid, tracks[i].BeadID, st.Children)
		}
		updateTrackExecutions(s, pid, steps, tracks[i].Children)
	}
}
