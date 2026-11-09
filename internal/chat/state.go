package chat

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/example-user/commander/internal/store"
)

// stateTracker derives the agent state (the agent_state table's vocabulary)
// purely from the event stream of one role.
type stateTracker struct {
	running bool // a process is up
	turn    bool // a turn is in flight
	crashed string
	tool    string            // detail of the tool running now
	pending map[string]string // request id -> tool name
	order   []string          // pending ids in arrival order
	tasks   map[string]string // running background tasks: their descriptions
	taskIDs []string          // and the order they started in
}

func newStateTracker() *stateTracker {
	return &stateTracker{pending: map[string]string{}, tasks: map[string]string{}}
}

// observe folds one event into the tracker.
func (t *stateTracker) observe(e Event) {
	if e.Source == SourceTranscript {
		return
	}
	switch e.Kind {
	case KindSession:
		t.running, t.crashed = true, ""
	case KindUser:
		if e.ParentToolUseID == "" && (e.Origin == "user" || e.Origin == "mail") {
			t.turn = true
		}
	case KindStatus:
		if e.Status == "requesting" || e.Status == "thinking" {
			t.turn = true
		}
	case KindToolInput:
		if e.ParentToolUseID == "" && e.Tool != nil {
			t.tool = toolDetail(e.Tool.Name, e.Tool.Input)
			t.turn = true
		}
	case KindToolResult:
		if e.ParentToolUseID == "" {
			t.tool = ""
		}
	case KindPermission, KindQuestion, KindPlan:
		if e.Request != nil {
			if _, ok := t.pending[e.Request.ID]; !ok {
				t.order = append(t.order, e.Request.ID)
			}
			t.pending[e.Request.ID] = e.Request.ToolName
		}
	case KindResolved:
		if e.Request != nil {
			t.resolve(e.Request.ID)
		}
	case KindTask:
		if e.Task == nil {
			break
		}
		id := firstNonEmpty(e.Task.TaskID, e.Task.ToolUseID)
		switch {
		case e.Subtype == "task_started":
			if _, ok := t.tasks[id]; !ok {
				t.taskIDs = append(t.taskIDs, id)
			}
			t.tasks[id] = e.Task.Description
		case e.Subtype == "task_notification" || isTerminalTaskStatus(e.Task.Status):
			delete(t.tasks, id)
			t.taskIDs = slices.DeleteFunc(t.taskIDs, func(x string) bool { return x == id })
		}
	case KindResult:
		t.turn, t.tool = false, ""
		t.pending, t.order = map[string]string{}, nil
	case KindExit:
		if t.turn && e.Exit != nil && !e.Exit.Expected {
			t.crashed = fmt.Sprintf("process exited mid-turn (code %d)", e.Exit.Code)
			if e.Exit.Signal != "" {
				t.crashed = "process killed mid-turn (" + e.Exit.Signal + ")"
			}
		}
		t.running, t.turn, t.tool = false, false, ""
		t.pending, t.order, t.tasks, t.taskIDs = map[string]string{}, nil, map[string]string{}, nil
	}
}

func (t *stateTracker) resolve(id string) {
	delete(t.pending, id)
	for i, o := range t.order {
		if o == id {
			t.order = append(t.order[:i], t.order[i+1:]...)
			break
		}
	}
}

func isTerminalTaskStatus(s string) bool {
	switch s {
	case "completed", "failed", "stopped", "killed", "cancelled":
		return true
	}
	return false
}

// state is the agent state and detail right now.
func (t *stateTracker) state() State {
	switch {
	case t.crashed != "":
		return State{State: store.AgentStuck, Detail: t.crashed}
	case len(t.order) > 0:
		name := t.pending[t.order[0]]
		switch name {
		case "AskUserQuestion":
			return State{State: store.AgentWaiting, Detail: "needs you: question"}
		case "ExitPlanMode":
			return State{State: store.AgentWaiting, Detail: "needs you: plan"}
		}
		return State{State: store.AgentWaiting, Detail: "needs you: approve " + name}
	case t.turn:
		return State{State: store.AgentWorking, Detail: t.tool}
	case len(t.tasks) > 0:
		return State{State: store.AgentWaiting, Detail: t.waitingOn()}
	}
	return State{State: store.AgentIdle}
}

// waitingOn says what an agent whose turn ended waits on, to read after
// "waiting on": its background tasks by what they do ("#4110 CI to finish"
// from "Wait for #4110 CI to finish"), the first one and how many more.
func (t *stateTracker) waitingOn() string {
	first := ""
	if len(t.taskIDs) > 0 {
		first = strings.TrimSpace(t.tasks[t.taskIDs[0]])
	}
	switch {
	case first == "":
		if len(t.tasks) == 1 {
			return "a background task"
		}
		return fmt.Sprintf("%d background tasks", len(t.tasks))
	case strings.HasPrefix(strings.ToLower(first), "wait for "):
		first = first[len("wait for "):]
	case strings.HasPrefix(strings.ToLower(first), "wait until "):
		first = first[len("wait until "):]
	default:
		first = "“" + first + "”"
	}
	if n := len(t.tasks) - 1; n > 0 {
		return fmt.Sprintf("%s and %d more", first, n)
	}
	return first
}

// toolDetail matches the mail hook's PreToolUse detail ("Bash: go test ./...").
func toolDetail(name string, input json.RawMessage) string {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	for _, key := range []string{"command", "file_path", "pattern", "url", "description"} {
		if v, ok := in[key].(string); ok && v != "" {
			v = strings.Join(strings.Fields(v), " ")
			if r := []rune(v); len(r) > 60 {
				v = string(r[:59]) + "…"
			}
			return name + ": " + v
		}
	}
	return name
}
