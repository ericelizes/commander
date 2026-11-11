package livedata

import (
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"maps"
)

// One current, identity-fenced set feeds the fleet, row count and spinner.
// A logical planning chat is not a claimed execution, regardless of its name.
func observedExecutionOf(s Snapshot, data Data) []SnapExecution {
	type scope struct {
		project int64
		bead    string
	}
	visible := map[scope]bool{}
	for _, ws := range s.Workstreams {
		visible[scope{ws.ProjectID, ws.BeadID}] = true
		for st := range stepValues(ws.Steps) {
			visible[scope{ws.ProjectID, st.BeadID}] = true
		}
	}
	out := []SnapExecution{}
	for _, fact := range data.ObservedExecutions {
		if !fact.Known || !fact.Alive {
			continue
		}
		bead := fact.BeadID
		if !visible[scope{fact.ProjectID, bead}] {
			for _, ancestor := range fact.Path {
				if visible[scope{fact.ProjectID, ancestor}] {
					bead = ancestor
					break
				}
			}
		}
		state := fact.State
		if state == "" {
			state = "unknown"
		}
		out = append(out, SnapExecution{Role: fact.Role, State: state, ProjectID: fact.ProjectID, BeadID: bead, TaskBeadID: fact.BeadID, RunID: fact.RunID, SessionID: fact.SessionID})
	}
	return out
}

func markObservedExecutionOwners(rows []ObjectiveRow, facts []store.ObservedExecution) []ObjectiveRow {
	type key struct {
		project int64
		bead    string
	}
	byID := map[key]store.ObservedExecution{}
	for _, fact := range facts {
		byID[key{fact.ProjectID, fact.BeadID}] = fact
	}
	out := append([]ObjectiveRow(nil), rows...)
	for i := range out {
		row := &out[i]
		fact, ok := byID[key{row.ProjectID, row.BeadID}]
		if !ok {
			continue
		}
		row.Owner = fact.Role
		switch {
		case fact.Known && fact.Alive && fact.State == store.AgentWorking:
			row.OwnerState = OwnerWorking
			row.OwnerWhy = ""
		case fact.Known && fact.Alive:
			row.OwnerState = "waiting"
			row.OwnerWhy = "Execution is present; waiting for its next action"
		default:
			row.OwnerState = "unverified"
			row.OwnerWhy = "Checking the recorded execution against its process"
		}
	}
	return out
}

func maskObservedAgentStates(data *Data) {
	data.Agents = maps.Clone(data.Agents)
	if data.Agents == nil {
		data.Agents = map[string]store.AgentState{}
	}
	for _, fact := range data.ObservedExecutions {
		state := fact.State
		if !fact.Known || !fact.Alive {
			state = "unknown"
		}
		for role, old := range data.Agents {
			if beads.OwnerLabelName(role) == beads.OwnerLabelName(fact.Role) {
				old.State = state
				old.Detail = fact.Detail
				old.Since = fact.Since
				data.Agents[role] = old
			}
		}
		old := data.Agents[fact.Role]
		old.Role = fact.Role
		old.State = state
		old.Detail = fact.Detail
		old.Since = fact.Since
		data.Agents[fact.Role] = old
	}
}

func unverifiedExecutionOf(s Snapshot, data Data) []SnapExecution {
	clone := data
	clone.ObservedExecutions = nil
	for _, fact := range data.ObservedExecutions {
		if !fact.Known || !fact.Alive {
			fact.Known = true
			fact.Alive = true
			fact.State = "unverified"
			clone.ObservedExecutions = append(clone.ObservedExecutions, fact)
		}
	}
	return observedExecutionOf(s, clone)
}

func applyObservedWorking(s *Snapshot) {
	type key struct {
		pid  int64
		bead string
	}
	working := map[key]bool{}
	for _, x := range s.Executions {
		if x.State == store.AgentWorking {
			working[key{x.ProjectID, x.BeadID}] = true
		}
	}
	var walk func(int64, []SnapStep) bool
	walk = func(pid int64, steps []SnapStep) bool {
		any := false
		for i := range steps {
			step := &steps[i]
			child := walk(pid, step.Children)
			step.Working = working[key{pid, step.BeadID}] || child
			any = any || step.Working
		}
		return any
	}
	for i := range s.Workstreams {
		ws := &s.Workstreams[i]
		child := walk(ws.ProjectID, ws.Steps)
		ws.Working = working[key{ws.ProjectID, ws.BeadID}] || child
	}
}

func executionScopeContains(id string, steps []SnapStep, bead string) bool {
	if id == bead {
		return true
	}
	for st := range stepValues(steps) {
		if st.BeadID == bead {
			return true
		}
	}
	return false
}
func waitingExecutionText(s Snapshot, pid int64, id string, steps []SnapStep) string {
	for _, x := range s.ExecutionChecks {
		if x.ProjectID == pid && executionScopeContains(id, steps, x.BeadID) {
			return "Checking execution status"
		}
	}
	for _, x := range s.Executions {
		if x.ProjectID == pid && executionScopeContains(id, steps, x.BeadID) {
			return "Execution is waiting for its next action"
		}
	}
	return "Queued for Commander"
}
