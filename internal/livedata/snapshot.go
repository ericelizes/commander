package livedata

import (
	"cmp"
	"context"
	"errors"
	"log"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/workflowtemplate"
)

// Snapshot is the JSON view of the cockpit served by cmdrd's /api/snapshot
// and /api/events: what the TUI sidebar and title bar show, as data.
type Snapshot struct {
	OrchestrationMode string            `json:"orchestration_mode,omitempty"`
	Storage           string            `json:"storage,omitempty"`
	Executions        []SnapExecution   `json:"executions"`
	ExecutionChecks   []SnapExecution   `json:"execution_checks,omitempty"` // recorded claims without current positive process evidence
	PRStates          map[string]string `json:"pr_states,omitempty"`
	Answers           map[string]int    `json:"answers,omitempty"`
	// PreserveVersion marks a failed project read served from last-good rows.
	PreserveVersion bool `json:"-"`
	// KeptLastGood marks a read that overlapped writes, served quietly from last-good
	// rows: not degraded and no mail, but the rows may not show a write just made.
	KeptLastGood bool `json:"-"`
	// Pending lists steps the board shows as a write the door answered left them, before a load
	// has read them back (Patched); a load that shows the write clears it.
	Pending []string `json:"pending,omitempty"`
	// ProjectTimes is how long each project's beads read took in this load.
	ProjectTimes map[string]time.Duration `json:"-"`
	// Phases is where this load's time went.
	Phases LoadPhases `json:"-"`
	// Version increases by one every time the content changes (set by the
	// server, not by BuildSnapshot).
	Version     uint64               `json:"version"`
	GeneratedAt time.Time            `json:"generated_at"`
	Projects    []SnapProject        `json:"projects"`
	Workstreams []SnapWorkstream     `json:"workstreams"` // the user's rank order; N is the sidebar number
	NeedsYou    []NeedsYou           `json:"needs_you"`
	NoPitch     []NoPitch            `json:"no_pitch,omitempty"` // ready asks with no pitch yet: the Manager's move, never NEEDS YOU
	Agents      map[string]SnapAgent `json:"agents"`             // by mail role: "commander", "manager-<bead>", ...
	Fleet       SnapFleet            `json:"fleet"`
	Unread      map[string]int       `json:"unread"` // unread mail per role; a role is absent when unknown
	// Watch is what cmdrd's minute watch sees of each Manager's running
	// Operators, by the Manager's role; a Manager is absent when it has none.
	Watch map[string]SnapWatch `json:"watch,omitempty"`
	// Broken lists the open beads that break the open-bead invariant (no
	// agent, no recorded wait, not the user's); each such step also has Broken set.
	Broken []Violation `json:"broken,omitempty"`
	Error  string      `json:"error,omitempty"`
	// Degraded is set when cmdrd cannot read its state (the store or beads
	// failed to load): everything else in the snapshot is stale or empty.
	Degraded *SnapDegraded `json:"degraded,omitempty"`
	// Health lists what keeps failing in cmdrd right now (mail that cannot reach
	// an agent, a Manager that cannot start): each a problem until it clears.
	Health []SnapHealth `json:"health,omitempty"`
	// Live is what cmdrd saw running when it built the snapshot; the board
	// checks every claim against it (nil: not known, claims are taken as recorded).
	Live *SnapLive `json:"live,omitempty"`
	// Display is the board derived from all of the above: what the sidebar
	// shows and the agents read (cmdrd sets it; Board derives it otherwise).
	Display *Display `json:"display,omitempty"`
	// Wrong is every place the screen disagrees with what is happening
	// (cmdrd sets it): the health badge lists it and Commander's board carries it.
	Wrong []SnapWrong `json:"wrong,omitempty"`
	// BoardVersions is the version of each role's board file (boardturn) that
	// this display was written as, so the app can say which one the user sees.
	BoardVersions map[string]int `json:"board_versions,omitempty"`
}

// SnapDegraded says why the snapshot cannot be trusted.
type SnapDegraded struct {
	Reason string `json:"reason"`
}

// SnapWrong is one mismatch between the screen and what is happening; Target
// is the workstream key that shows it ("" when none does).
type SnapWrong struct {
	Key    string `json:"key"`
	Target string `json:"target,omitempty"`
	Text   string `json:"text"`
	Agent  string `json:"-"` // Commander's board line when it must differ from the badge's Text
}

// SnapHealth is one failure that has kept happening past the health grace:
// what is broken, in plain words, and since when.
type SnapHealth struct {
	Key    string `json:"key"`
	Detail string `json:"detail"`
	Since  int64  `json:"since"`
}

// SnapWatch is one Manager's running Operators and its next check-in on them.
type SnapWatch struct {
	CheckedAt int64           `json:"checked_at,omitempty"` // unix s of the watch's last look
	CheckinAt int64           `json:"checkin_at,omitempty"` // unix s of the Manager's next check-in
	Operators []SnapWatchedOp `json:"operators"`
	// Finished: Operators whose process ended in the last few minutes.
	Finished []SnapFinishedOp `json:"finished,omitempty"`
	// Issues (Commander's watch only): what is wrong across workstreams now.
	Issues []SnapIssue `json:"issues,omitempty"`
}

// SnapIssue is something wrong with a workstream that has lasted long enough to matter.
type SnapIssue struct {
	Workstream string `json:"workstream"` // its bead id
	Text       string `json:"text"`       // "stalled: nobody on its open work"
	Since      int64  `json:"since"`      // unix s it began
}

// SnapWatchedOp is one running Operator under watch.
type SnapWatchedOp struct {
	Role     string `json:"role"`
	BeadID   string `json:"bead_id"`
	ActiveAt int64  `json:"active_at,omitempty"` // unix s of its last transcript write
	Since    int64  `json:"since"`               // unix s it was spawned
}

// SnapFinishedOp is an Operator that ended recently: finished with a report
// (Died false; Report is its first line) or died.
type SnapFinishedOp struct {
	Role   string `json:"role"`
	BeadID string `json:"bead_id"`
	At     int64  `json:"at"` // unix s it ended
	Died   bool   `json:"died,omitempty"`
	Report string `json:"report,omitempty"`
}

// SnapProject is one activated project.
type SnapProject struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Path   string `json:"path"`
	Status string `json:"status"`
	Repo   string `json:"repo,omitempty"` // GitHub owner/name from its origin remote, for PR links ("" = not on GitHub)
}

// SnapWorkstream is one workstream with its step tree.
type SnapWorkstream struct {
	Withdrawn            bool            `json:"withdrawn,omitempty"`   // a cancelled request
	ProgressAt           int64           `json:"progress_at,omitempty"` // last recorded progress (unix s)
	TypedWaits           []beads.Wait    `json:"-"`                     // the root's recorded waits
	PlanningPending      bool            `json:"planning_pending,omitempty"`
	ResultRunID          int64           `json:"result_run_id,omitempty"`
	ResultReadyAt        int64           `json:"result_ready_at,omitempty"`
	BlockedBy            []string        `json:"blocked_by,omitempty"`
	QuestionWaiting      bool            `json:"question_waiting,omitempty"`
	HumanAnswerPending   bool            `json:"human_answer_pending,omitempty"`
	ExecutionMode        string          `json:"execution_mode,omitempty"`
	ExecutionReady       bool            `json:"execution_ready,omitempty"`
	ExecutionEligible    bool            `json:"execution_eligible,omitempty"`
	ExecutionReserved    bool            `json:"execution_reserved,omitempty"`
	ExecutionCapable     bool            `json:"execution_capable,omitempty"`
	ExecutionAttempts    int64           `json:"execution_attempts,omitempty"`
	ExecutionError       string          `json:"execution_error,omitempty"`
	PlanningCommander    string          `json:"planning_commander,omitempty"`
	JurisdictionRevision int64           `json:"jurisdiction_revision,omitempty"`
	N                    int             `json:"n"`
	Project              string          `json:"project"`
	Repo                 string          `json:"repo,omitempty"` // the project's GitHub owner/name: bare pr:N labels resolve through it
	ProjectID            int64           `json:"project_id"`
	BeadID               string          `json:"bead_id"`
	Title                string          `json:"title"`
	Tickets              []string        `json:"tickets,omitempty"` // Linear ids of its ticket: labels
	State                string          `json:"state"`
	Working              bool            `json:"working,omitempty"` // an agent under it is working now
	Status               string          `json:"status"`
	Owner                string          `json:"owner,omitempty"`
	Starred              bool            `json:"starred,omitempty"`
	Hold                 bool            `json:"hold,omitempty"`        // the root bead has the hold label; a hold lower down is not here
	HoldReason           string          `json:"hold_reason,omitempty"` // from its latest "hold by" comment
	Review               string          `json:"review,omitempty"`
	Ask                  string          `json:"ask,omitempty"`
	Note                 string          `json:"note,omitempty"`
	Decision             *store.Decision `json:"decision,omitempty"`
	ManagerRole          string          `json:"manager_role"`
	NowStep              string          `json:"now_step,omitempty"` // bead id of the step the Manager is on
	Done                 int             `json:"done"`
	Total                int             `json:"total"`
	Steps                []SnapStep      `json:"steps"`
	// The workstream bead's own waits (waiting:... labels), as on a step.
	Waiting     string           `json:"waiting,omitempty"`
	WaitSince   int64            `json:"waiting_since,omitempty"`
	WaitUntil   int64            `json:"waiting_until,omitempty"`
	WaitUntilAs string           `json:"waiting_until_as,omitempty"`
	WaitUntils  map[string]int64 `json:"waiting_untils,omitempty"` // every time wait: text -> unix s
	// NoGates: the workstream is open and neither it nor any stage has a gate.
	NoGates bool `json:"no_gates,omitempty"`
	// GateNodes: every gate of the workstream as a node of the bead graph.
	GateNodes []SnapGateNode `json:"gate_nodes,omitempty"`
}

// SnapGateNode is a stage gate as a node of the bead graph (kind "gate"): it
// waits on its stage (BlockedBy) and the steps that wait on that stage wait on
// it (Blocks). Gates stay on their step too (SnapStep.Gates).
type SnapGateNode struct {
	Kind      string   `json:"kind"` // always "gate"
	BeadID    string   `json:"bead_id"`
	Stage     string   `json:"stage"` // the step bead the gate guards
	Name      string   `json:"name"`
	Verifier  string   `json:"verifier"`
	Status    string   `json:"status"` // open | passed | failed; passed = the bead is closed
	Check     string   `json:"check,omitempty"`
	Evidence  string   `json:"evidence,omitempty"`
	By        string   `json:"by,omitempty"`
	At        int64    `json:"at,omitempty"`
	BlockedBy []string `json:"blocked_by"`
	Blocks    []string `json:"blocks"`
	Viz       int      `json:"viz"` // its level in the Graph: 1 gates, 2 stages, 3 everything (label viz:<n>, default 1)
}

// SnapGate is one check a stage must pass before it closes.
type SnapGate struct {
	BeadID   string `json:"bead_id,omitempty"` // the gate's own bead (see SnapGateNode)
	Name     string `json:"name"`
	Verifier string `json:"verifier"` // auto | operator | reviewer:<login> | user
	Status   string `json:"status"`   // open | passed | failed
	Evidence string `json:"evidence,omitempty"`
	By       string `json:"by,omitempty"`
	At       int64  `json:"at,omitempty"` // unix seconds of the last result
}

// SnapStep is one step; phases carry their children and a one-line note.
type SnapStep struct {
	Hold                 bool                    `json:"hold,omitempty"`        // the hold label on this bead
	HoldReason           string                  `json:"hold_reason,omitempty"` // from its latest "hold by" comment
	Withdrawn            bool                    `json:"withdrawn,omitempty"`   // a cancelled request
	ProgressAt           int64                   `json:"progress_at,omitempty"` // last recorded progress (unix s)
	TypedWaits           []beads.Wait            `json:"-"`                     // the recorded waits behind Waiting
	PlanningPending      bool                    `json:"planning_pending,omitempty"`
	ResultRunID          int64                   `json:"result_run_id,omitempty"`
	ResultReadyAt        int64                   `json:"result_ready_at,omitempty"`
	QuestionWaiting      bool                    `json:"question_waiting,omitempty"`
	HumanAnswerPending   bool                    `json:"human_answer_pending,omitempty"`
	ExecutionMode        string                  `json:"execution_mode,omitempty"`
	ExecutionEligible    bool                    `json:"execution_eligible,omitempty"`
	ExecutionReady       bool                    `json:"execution_ready,omitempty"`
	ExecutionReserved    bool                    `json:"execution_reserved,omitempty"`
	ExecutionCapable     bool                    `json:"execution_capable,omitempty"`
	ExecutionAttempts    int64                   `json:"execution_attempts,omitempty"`
	ExecutionError       string                  `json:"execution_error,omitempty"`
	PlanningCommander    string                  `json:"planning_commander,omitempty"`
	JurisdictionRevision int64                   `json:"jurisdiction_revision,omitempty"`
	Activity             *StatusEvidence         `json:"activity,omitempty"`
	WorkflowCheck        *workflowtemplate.Node  `json:"workflow_check,omitempty"`
	GateChecks           []workflowtemplate.Node `json:"gate_checks,omitempty"`
	BeadID               string                  `json:"bead_id"`
	Title                string                  `json:"title"`
	State                string                  `json:"state"`
	Working              bool                    `json:"working,omitempty"` // an agent under it is working now
	Status               string                  `json:"status"`
	Owner                string                  `json:"owner,omitempty"`
	OwnerState           string                  `json:"owner_state,omitempty"` // its Operator: working | finished (exited, reported) | died (exited, step still open)
	OwnerWhy             string                  `json:"owner_why,omitempty"`   // why, when cmdrd corrected the claim against what runs or settled a death
	OwnerSince           int64                   `json:"owner_since,omitempty"` // unix s its Operator stopped, when known
	Restarts             int                     `json:"restarts,omitempty"`    // automatic restarts cmdrd made of its Operator
	Starred              bool                    `json:"starred,omitempty"`
	Review               string                  `json:"review,omitempty"` // PR number while in review ("?" = unknown)
	// ReviewFact is what GitHub says now about the reviews this step waits on,
	// looked up live by cmdrd rather than recorded by an agent.
	PRRepo      string           `json:"pr_repo,omitempty"`
	Tickets     []string         `json:"tickets,omitempty"` // Linear ids of its ticket: labels
	PR          []string         `json:"pr,omitempty"`      // PR numbers of its pr: labels (a gate held by a card is matched on them)
	ReviewFact  *ReviewFact      `json:"review_fact,omitempty"`
	Ask         string           `json:"ask,omitempty"`
	Note        string           `json:"note,omitempty"`
	Decision    *store.Decision  `json:"decision,omitempty"`
	Now         bool             `json:"now,omitempty"`
	PhaseNote   string           `json:"phase_note,omitempty"`
	Children    []SnapStep       `json:"children,omitempty"`
	BlockedBy   []string         `json:"blocked_by,omitempty"`       // bead IDs it waits on
	Until       string           `json:"until,omitempty"`            // owned job: its finish line; its children are the owner's pipeline
	Gate        bool             `json:"gate,omitempty"`             // a human approval gate: approved when closed
	Approvers   []string         `json:"approvers,omitempty"`        // who approves the gate
	Waiting     string           `json:"waiting,omitempty"`          // what its owner waits on: "CI #4101", "review (reviewer-a, reviewer-b)", "you"
	WaitSince   int64            `json:"waiting_since,omitempty"`    // unix s its oldest wait was recorded
	WaitChecked int64            `json:"waiting_checked,omitempty"`  // unix s cmdrd last looked at its waits (no wake)
	WaitUntil   int64            `json:"waiting_until,omitempty"`    // unix s of its earliest time wait (waiting:...:until:...)
	WaitUntilAs string           `json:"waiting_until_as,omitempty"` // that time wait's text, as it appears in Waiting
	WaitUntils  map[string]int64 `json:"waiting_untils,omitempty"`   // every time wait: text -> unix s (WaitUntil is only the earliest)
	ParkedUntil int64            `json:"parked_until,omitempty"`     // unix s a parked (deferred) step comes back; the row reads "Parked until <time>"
	Gates       []SnapGate       `json:"gates,omitempty"`            // checks it must pass before it closes
	NoGates     bool             `json:"no_gates,omitempty"`         // an open stage (top-level step) with no gates
	Viz         int              `json:"viz"`                        // its level in the Graph: 1 gates, 2 stages, 3 everything (label viz:<n>, else gate 1, stage 2, task 3)
	Superseded  bool             `json:"superseded,omitempty"`       // dropped from the plan: not drawn, never approved
	Broken      bool             `json:"broken,omitempty"`           // open with no agent, wait or the user on it: "State missing"
	Task        bool             `json:"task,omitempty"`             // an Operator task bead: agent-internal, left out of the workflow and diagram (its step shows its state)
	Track       bool             `json:"track,omitempty"`            // a track of its workstream: a separate outcome with its own gate chain and status line
	// ManagerRole is set on a nested workstream that has a Manager of its own
	// (its project's nested Managers are on): manager-<its bead>.
	ManagerRole string       `json:"manager_role,omitempty"`
	Workstream  string       `json:"workstream,omitempty"`   // a track's workstream bead id
	Lane        string       `json:"lane,omitempty"`         // its lane label (you, agents, elsewhere[:who]) where nothing else says whose move it is
	Criteria    string       `json:"criteria,omitempty"`     // what counts as passed (acceptance criteria): a gate's criteria
	CloseReason string       `json:"close_reason,omitempty"` // a closed bead's close reason (capped): what it ended with
	PushCount   int          `json:"push_count,omitempty"`
	PushFirst   int64        `json:"push_first,omitempty"`
	SpecHash    string       `json:"spec_hash,omitempty"` // fingerprint of its title+description+acceptance now
	Basis       *store.Basis `json:"basis,omitempty"`     // what its open card was raised against (nil = before it was recorded)

	// What a closed step folded away and a reader still needs (attachFoldedOutcomes).
	FinishedTasks []FinishedTask `json:"finished_tasks,omitempty"` // the latest finished tasks under this closed step
	DoneBlockers  []string       `json:"done_blockers,omitempty"`  // blocked_by ids that are finished and not drawn: they hold nothing
}

// SnapAgent is an agent's live state. State is already corrected for
// staleness (a silent "working" agent reads "idle").
type SnapAgent struct {
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
	Since  int64  `json:"since"` // unix seconds of the last state change
	// Wait is what an otherwise idle agent's turn ended on ("CI #4101",
	// "review (reviewer-a, reviewer-b)", "you"); the state then reads "waiting".
	Wait string `json:"wait,omitempty"`
}

// SnapFleet is the fleet metrics card.
type SnapFleet struct {
	CostUSD24h    float64   `json:"cost_usd_24h"`
	CostUSD7d     float64   `json:"cost_usd_7d"`
	CostSeries24h []float64 `json:"cost_series_24h"`
	Agents        int       `json:"agents"`
	Active        int       `json:"active"`
	Blocked       int       `json:"blocked"`
	IdleRatio     float64   `json:"idle_ratio"`
	Projects      int       `json:"projects"`
	// ChatCostUSD24h is the Commander+Manager part of CostUSD24h; Roles has
	// each chat role's estimated spend (plan-equivalent, not billed).
	ChatCostUSD24h float64        `json:"chat_cost_usd_24h"`
	Roles          []SnapRoleCost `json:"roles_cost"`
}

// SnapRoleCost is one chat role's spend: today, its current session, all time.
type SnapRoleCost struct {
	Role         string  `json:"role"`
	TodayUSD     float64 `json:"today_usd"`
	SessionUSD   float64 `json:"session_usd"`
	TotalUSD     float64 `json:"total_usd"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
}

// BuildSnapshot turns loaded Data plus unread counts into a Snapshot.
func BuildSnapshot(data Data, unread map[string]int) Snapshot {
	data.workstreams = Workstreams(data)
	if data.workstreams == nil {
		data.workstreams = []Workstream{}
	}
	asks, noPitch := askLists(data)
	s := Snapshot{
		OrchestrationMode: data.OrchestrationMode,
		GeneratedAt:       data.LoadedAt,
		Projects:          []SnapProject{},
		Workstreams:       []SnapWorkstream{},
		NeedsYou:          asks,
		NoPitch:           noPitch,
		Agents:            map[string]SnapAgent{},
		Unread:            map[string]int{},
		Fleet: SnapFleet{
			CostUSD24h: data.Metrics.TotalCostUSD24h, CostUSD7d: data.Metrics.TotalCostUSD7d,
			CostSeries24h: data.Metrics.CostSeries24h, Agents: data.Metrics.AgentCount,
			Active: data.Metrics.ActiveCount, Blocked: data.Metrics.BlockedCount,
			IdleRatio: data.Metrics.IdleRatio, Projects: data.Metrics.ProjectCount,
		},
	}
	s.Fleet.Roles = []SnapRoleCost{}
	s.Fleet.ChatCostUSD24h = data.Metrics.ChatCostUSD24h
	for _, r := range data.Metrics.RoleCosts {
		s.Fleet.Roles = append(s.Fleet.Roles, SnapRoleCost(r))
	}
	if data.LoadErr != nil {
		s.Error = data.LoadErr.Error()
		s.Degraded = &SnapDegraded{Reason: data.LoadErr.Error()}
	}
	for _, p := range data.Projects {
		s.Projects = append(s.Projects, SnapProject{ID: p.ID, Name: p.Name, Type: p.Type, Path: p.Path, Status: p.Status, Repo: GitHubRepo(p.Path)})
	}
	live := func(owner string) string {
		a, ok := data.Agents[owner]
		if !ok {
			return ""
		}
		if data.Postgres {
			return a.State
		}
		return EffectiveState(a, data.LoadedAt)
	}
	for i, ws := range Workstreams(data) {
		s.Workstreams = append(s.Workstreams, snapWorkstream(i+1, ws, live))
	}
	for role, a := range data.Agents {
		state := EffectiveState(a, data.LoadedAt)
		if data.Postgres {
			state = a.State
		}
		s.Agents[role] = SnapAgent{State: state, Detail: a.Detail, Since: a.Since}
	}
	foldWaits(&s, Workstreams(data))
	s.Broken = OpenBeadInvariantAt(data.Objectives, live, data.LoadedAt)
	markBroken(s.Workstreams, s.Broken)
	s.Watch = watchOf(data)
	for role, n := range unread {
		s.Unread[role] = n
	}
	return s
}

// ManagerWait is what a workstream's Manager waits on: the text and the
// moment the oldest wait began.
type ManagerWait struct {
	Text  string
	Since time.Time
}

// ManagerWaits collects the recorded waits of every open step of each
// workstream, by its Manager's mail role.
func ManagerWaits(workstreams []Workstream) map[string]ManagerWait {
	out := map[string]ManagerWait{}
	for _, ws := range workstreams {
		byRole := map[string][]beads.Wait{}
		var roles []string
		add := func(role string, w []beads.Wait) {
			if len(w) == 0 {
				return
			}
			if _, ok := byRole[role]; !ok {
				roles = append(roles, role)
			}
			byRole[role] = append(byRole[role], w...)
		}
		byID := map[string]ObjectiveRow{ws.Root.BeadID: ws.Root}
		for _, n := range ws.Steps {
			byID[n.Row.BeadID] = n.Row
		}
		for _, n := range ws.Steps {
			if n.Row.Status != "closed" && n.Row.Status != beads.StatusDeferred {
				// A nested workstream's waits are its own Manager's.
				add(store.WorkstreamManagerRole(ownerIn(byID, n.Row.BeadID)), n.Row.Waits)
			}
		}
		if ws.Root.Status != "closed" && ws.Root.Status != beads.StatusDeferred {
			add(store.WorkstreamManagerRole(ws.Root.BeadID), ws.Root.Waits)
		}
		for _, role := range roles {
			waits := byRole[role]
			oldest := waits[0].Since
			for _, w := range waits {
				if w.Since.Before(oldest) {
					oldest = w.Since
				}
			}
			out[role] = ManagerWait{Text: beads.WaitsText(waits), Since: oldest}
		}
	}
	return out
}

// foldWaits shows a Manager that ended its turn on a recorded wait as
// "waiting" (not idle), with what it waits on and since when. A working Manager
// keeps its state and only carries the wait text.
func foldWaits(s *Snapshot, workstreams []Workstream) {
	for role, w := range ManagerWaits(workstreams) {
		a := s.Agents[role]
		a.Wait = w.Text
		if a.State == "" || a.State == store.AgentIdle {
			a.State, a.Detail, a.Since = store.AgentWaiting, w.Text, w.Since.Unix()
		}
		s.Agents[role] = a
	}
}

func snapWorkstream(n int, ws Workstream, live LiveAgent) SnapWorkstream {
	r := ws.Root
	roots := BuildStepTree(ws.Steps)
	now := NowStep(roots)
	state, working := DerivedLive(&StepNode{Row: r, Children: roots}, live)
	if mgr := store.WorkstreamManagerRole(r.BeadID); live != nil && live(mgr) == store.AgentWorking {
		working = true
	}
	out := SnapWorkstream{
		ResultRunID: r.ResultRunID, ResultReadyAt: r.ResultReadyAt, QuestionWaiting: r.QuestionWaiting, HumanAnswerPending: r.HumanAnswerPending,
		ExecutionMode: r.ExecutionMode, ExecutionReady: r.ExecutionReady, ExecutionEligible: r.ExecutionEligible, ExecutionReserved: r.ExecutionReserved, ExecutionCapable: r.ExecutionCapable, ExecutionAttempts: r.ExecutionAttempts, ExecutionError: r.ExecutionError,
		PlanningPending: r.PlanningPending, PlanningCommander: r.PlanningCommander, JurisdictionRevision: r.JurisdictionRevision,
		BlockedBy: slices.Clone(r.BlockedBy), N: n, Project: ws.Project.Name, Repo: GitHubRepo(ws.Project.Path), ProjectID: ws.Project.ID, BeadID: r.BeadID, Title: r.Title, Tickets: r.Tickets,
		State: state, Working: working, Status: r.Status, Owner: r.Owner, Starred: r.Starred, Hold: r.Hold, HoldReason: r.HoldReason, Review: r.Review,
		Withdrawn: r.Withdrawn, ProgressAt: r.ProgressAt, TypedWaits: r.Waits,
		Ask: r.Ask, Note: r.Note, Decision: r.Decision,
		ManagerRole: store.WorkstreamManagerRole(r.BeadID), NowStep: now,
		Total: len(ws.Steps), Steps: snapSteps(roots, now, live, true),
	}
	own := SnapStep{}
	stepWaits(&own, r.Waits)
	out.Waiting, out.WaitSince, out.WaitUntil, out.WaitUntilAs, out.WaitUntils = beads.WaitsText(r.Waits), own.WaitSince, own.WaitUntil, own.WaitUntilAs, own.WaitUntils
	markWorkstream(out.Steps, r.BeadID)
	out.NoGates = state != beads.StateDone && r.Status != "closed" && r.Status != beads.StatusDeferred && len(r.Gates) == 0
	for _, st := range ws.Steps {
		if len(st.Row.Gates) > 0 {
			out.NoGates = false
		}
	}
	for _, st := range ws.Steps {
		if st.Row.State == beads.StateDone || st.Row.Status == "closed" {
			out.Done++
		}
	}
	out.GateNodes = snapGateNodes(ws)
	return out
}

// snapGateNodes lists the workstream's gates as graph nodes. Only gates that
// are beads are nodes; a gates-table row not yet migrated has no id.
func snapGateNodes(ws Workstream) []SnapGateNode {
	rows := []ObjectiveRow{ws.Root}
	for _, n := range ws.Steps {
		rows = append(rows, n.Row)
	}
	var out []SnapGateNode
	for _, r := range rows {
		for i, g := range r.Gates {
			if i >= len(r.GateIDs) || r.GateIDs[i] == "" {
				continue
			}
			node := SnapGateNode{
				Kind: "gate", BeadID: r.GateIDs[i], Stage: r.BeadID, Name: g.Name, Verifier: g.Verifier, Status: g.Status,
				Check: g.Check, Evidence: g.Evidence, By: g.By, At: g.At, BlockedBy: []string{r.BeadID}, Blocks: []string{}, Viz: 1,
			}
			if i < len(r.GateViz) && r.GateViz[i] > 0 {
				node.Viz = r.GateViz[i]
			}
			for _, o := range rows {
				if slices.Contains(o.BlockedBy, r.BeadID) {
					node.Blocks = append(node.Blocks, o.BeadID)
				}
			}
			out = append(out, node)
		}
	}
	return out
}

// stepWaits folds a bead's waits into the step's since/until/checked fields.
func stepWaits(st *SnapStep, waits []beads.Wait) {
	for _, w := range waits {
		if s := w.Since.Unix(); !w.Since.IsZero() && (st.WaitSince == 0 || s < st.WaitSince) {
			st.WaitSince = s
		}
		if u := w.Until.Unix(); w.Kind == beads.WaitUntil && (st.WaitUntil == 0 || u < st.WaitUntil) {
			st.WaitUntil, st.WaitUntilAs = u, w.Text()
		}
		if w.Kind == beads.WaitUntil {
			if st.WaitUntils == nil {
				st.WaitUntils = map[string]int64{}
			}
			st.WaitUntils[w.Text()] = w.Until.Unix()
		}
		if c := w.Checked.Unix(); !w.Checked.IsZero() && c > st.WaitChecked {
			st.WaitChecked = c
		}
	}
}

func snapSteps(nodes []*StepNode, now string, live LiveAgent, stage bool) []SnapStep {
	out := []SnapStep{}
	var stages map[string]bool
	if stage {
		stages = stageIDs(nodes)
	}
	for _, n := range nodes {
		o := n.Row
		st := SnapStep{
			Hold: o.Hold, HoldReason: o.HoldReason, Withdrawn: o.Withdrawn, ProgressAt: o.ProgressAt, TypedWaits: o.Waits,
			ResultRunID: o.ResultRunID, ResultReadyAt: o.ResultReadyAt, QuestionWaiting: o.QuestionWaiting, HumanAnswerPending: o.HumanAnswerPending,
			ExecutionMode: o.ExecutionMode, ExecutionEligible: o.ExecutionEligible, ExecutionReady: o.ExecutionReady, ExecutionReserved: o.ExecutionReserved, ExecutionCapable: o.ExecutionCapable, ExecutionAttempts: o.ExecutionAttempts, ExecutionError: o.ExecutionError,
			PlanningPending: o.PlanningPending, PlanningCommander: o.PlanningCommander, JurisdictionRevision: o.JurisdictionRevision,
			WorkflowCheck: o.WorkflowCheck, BeadID: o.BeadID, Title: o.Title, State: o.State, Status: o.Status, Owner: o.Owner, OwnerState: o.OwnerState,
			OwnerWhy: o.OwnerWhy, OwnerSince: o.OwnerSince, Restarts: o.Restarts,
			Starred: o.Starred, Review: o.Review, PR: o.PR, Tickets: o.Tickets, PRRepo: o.PRRepo, Ask: o.Ask, Note: o.Note, Decision: o.Decision, SpecHash: o.SpecHash, Basis: o.Basis, PushCount: o.PushCount, PushFirst: o.PushFirst,
			Now: o.BeadID != "" && o.BeadID == now, BlockedBy: o.BlockedBy, Until: o.Until, Gate: o.Gate, Approvers: o.Approvers,
			Superseded: o.Superseded, Lane: o.Lane, Criteria: o.Criteria, CloseReason: o.CloseReason, FinishedTasks: o.FinishedTasks, DoneBlockers: o.DoneBlockers, Waiting: beads.WaitsText(o.Waits), ParkedUntil: o.ParkedUntil, Gates: snapGates(o.Gates, o.GateIDs),
		}
		if o.Owns {
			st.ManagerRole = store.WorkstreamManagerRole(o.BeadID)
		}
		st.GateChecks = copyWorkflowNodes(o.GateChecks)
		if o.WorkflowCheck != nil {
			copy := copyWorkflowNode(*o.WorkflowCheck)
			st.WorkflowCheck = &copy
		}
		stepWaits(&st, o.Waits)
		st.Viz = EffectiveViz(o, stages[o.BeadID])
		st.Track = o.Track
		st.Task = o.Task
		st.NoGates = stage && len(o.Gates) == 0 && !o.Gate && o.State != beads.StateDone && o.Status != "closed" && o.Status != beads.StatusDeferred
		if o.Track && st.NoGates {
			st.NoGates = !hasGateBelow(n)
		}
		st.State, st.Working = DerivedLive(n, live)
		if len(n.Children) > 0 {
			st.PhaseNote = PhaseNote(n)
			st.Children = snapSteps(n.Children, now, live, false)
		}
		out = append(out, st)
	}
	return out
}

// markWorkstream stamps the top-level workstream on the workstreams nested under it.
func markWorkstream(steps []SnapStep, root string) {
	for i := range steps {
		if steps[i].Track {
			steps[i].Workstream = root
		}
		markWorkstream(steps[i].Children, root)
	}
}

func snapGates(gs []store.Gate, ids []string) []SnapGate {
	var out []SnapGate
	for i, g := range gs {
		id := ""
		if i < len(ids) {
			id = ids[i]
		}
		out = append(out, SnapGate{BeadID: id, Name: g.Name, Verifier: g.Verifier, Status: g.Status, Evidence: g.Evidence, By: g.By, At: g.At})
	}
	return out
}

// MailRoles is every role the cockpit shows mail for: Commander and each
// workstream's Manager.
func MailRoles(data Data) []string {
	roles := []string{"commander"}
	for _, ws := range Workstreams(data) {
		roles = append(roles, store.WorkstreamManagerRole(ws.Root.BeadID))
		for _, o := range workstreamOwners(ws) {
			roles = append(roles, store.WorkstreamManagerRole(o))
		}
	}
	return roles
}

// Source loads Snapshots. Unread mail is the expensive part (one mail-server
// call per role), so it is refreshed at most every UnreadEvery. Snapshot may be
// called concurrently (cmdrd's health loop beside an API load): loads overlap,
// since one can take minutes, and building from a load is one at a time. The
// callbacks run under that lock, so none may call Snapshot.
type Source struct {
	PRHeads     func(pr string) (workflowtemplate.Head, bool)
	Deps        Deps
	Unread      UnreadFunc // nil: no unread counts
	UnreadEvery time.Duration
	// WaitsChecked is when cmdrd last checked the recorded waits (nil or zero: unknown).
	WaitsChecked func() time.Time
	// ReviewFacts is GitHub's current word on each bead's review waits (nil: none).
	ReviewFacts func() map[string]ReviewFact
	PRStates    func() map[string]string
	// Chats is the state of each running native chat, by role (nil: none).
	Chats func() map[string]ChatState
	// Live lists the Operators running now (owner-label or agent names) and
	// when that was looked at; ok false means it could not tell (nil: no fact).
	Live func() (alive []string, at time.Time, ok bool)
	// Reported lists runs that ended with a report whose settle has not landed (nil = none).
	Reported func() []SnapReported
	// PitchAsked is when cmdrd mailed each NoPitch ask's Manager, by
	// "project:bead" (nil: none).
	PitchAsked func() map[string]int64
	// Health is cmdrd's current problems (nil: none tracked).
	Health func() []SnapHealth
	// OnSnapshot sees every snapshot built (cmdrd checks each plan against the rules).
	OnSnapshot func(snap *Snapshot)
	// Describe writes each active-mode row's one-liner from its state (nil: none).
	Describe DescribeFunc

	mu        sync.Mutex // guards what follows, and s.Deps.projectRows; never held during Load
	chatSince map[string]SnapAgent
	since     sinceTracker

	last     lastData
	unreadAt time.Time
	unread   map[string]int

	// recent is the latest clean PostgreSQL build, so readers that only need
	// current data (Last, the health loop) reuse it instead of loading again.
	recentMu   sync.Mutex
	recentData Data
	recentAt   time.Time
}

// recentFresh is how old a build Last may reuse. The API rebuilds within a
// second of any change the board reads, and at least every 30s, so a newer
// build exists whenever the board could have changed.
const recentFresh = 35 * time.Second

// Recent returns the latest clean PostgreSQL build if it is at most maxAge old.
// Callers must treat the returned Data as read-only: it is shared.
func (s *Source) Recent(maxAge time.Duration) (Data, bool) {
	s.recentMu.Lock()
	defer s.recentMu.Unlock()
	if s.recentAt.IsZero() || time.Since(s.recentAt) > maxAge {
		return Data{}, false
	}
	return s.recentData, true
}

// LoadTimes is what a slow load's log line says. Projects read slowest first; what
// the beads phase spent beyond the slowest project is waiting on the others.
func (s Snapshot) LoadTimes() string {
	names := slices.Sorted(maps.Keys(s.ProjectTimes))
	slices.SortStableFunc(names, func(a, b string) int { return cmp.Compare(s.ProjectTimes[b], s.ProjectTimes[a]) })
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+" "+s.ProjectTimes[name].Round(time.Millisecond).String())
	}
	read := "no project read"
	if len(parts) > 0 {
		read = "beads read per project: " + strings.Join(parts, ", ")
	}
	return read + "; phases: " + s.Phases.String()
}

// unreadTimeout bounds one role's unread count (one mail-server call).
const unreadTimeout = 2 * time.Second

// Snapshot loads the fleet and builds a Snapshot (Version left at 0).
func (s *Source) Snapshot(ctx context.Context) Snapshot {
	_, snapshot := s.Current(ctx)
	return snapshot
}

// Current returns one load and its canonical projection for presentation
// adapters. Terminal columns must not infer a second set of runtime states.
func (s *Source) Current(ctx context.Context) (Data, Snapshot) {
	start := time.Now()
	s.mu.Lock()
	lock := time.Since(start)
	if s.Deps.projectRows == nil {
		s.Deps.projectRows = &projectRowsCache{rows: map[projectRowsKey][]ObjectiveRow{}}
	}
	deps := s.Deps
	s.mu.Unlock()
	data := Load(ctx, deps)
	data.Phases.Lock = lock
	snapshot := s.build(ctx, data)
	if data.Postgres {
		frame := snapshot
		if data.LoadErr != nil || ctx.Err() != nil || frame.Degraded != nil {
			reason := "Current state is unavailable"
			if frame.Degraded != nil {
				reason = frame.Degraded.Reason
			}
			log.Printf("livedata: live state read failed: load=%v ctx=%v degraded=%q (%s)", data.LoadErr, ctx.Err(), reason, data.Phases)
			if data.LoadErr == nil {
				data.LoadErr = errors.New(reason)
			}
			frame = Snapshot{Storage: "postgres", Executions: []SnapExecution{}, Degraded: &SnapDegraded{Reason: reason}}
			data.Projects = nil
			data.Objectives = nil
			data.Operators = nil
			data.Agents = map[string]store.AgentState{}
		}
		data.CanonicalSnapshot = &frame
		if frame.Degraded == nil {
			s.recentMu.Lock()
			s.recentData, s.recentAt = data, time.Now()
			s.recentMu.Unlock()
		}
		data.Metrics.AgentCount = frame.Fleet.Agents
		data.Metrics.ActiveCount = frame.Fleet.Active
		data.Metrics.BlockedCount = frame.Fleet.Blocked
		data.Metrics.IdleRatio = frame.Fleet.IdleRatio
	}
	return data, snapshot
}

// build turns a load into a Snapshot.
// FinishBoard is the part of a full build that a board rebuilt after a write
// needs too: agent counts and the written lines.
func (s *Source) FinishBoard(snap *Snapshot, d *Display) {
	applyExecutionCounts(snap, d)
	if !s.stateOnly {
		applyDescriptions(s.Describe, snap, d)
	}
}

func (s *Source) build(ctx context.Context, data Data) Snapshot {
	start := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	data.Phases.Lock += time.Since(start)
	start = time.Now()
	// A load whose own context ended says nothing about the store: every read
	// after that point failed, most of them silently (no Operators, no asks, maybe
	// no gates), and LoadErr may even be nil. Its result is dropped whole, before
	// anything sees it: no last data, no error, no watcher, no board.
	if ctx.Err() != nil {
		return Snapshot{ProjectTimes: data.ProjectTimes, Phases: data.Phases, KeptLastGood: true}
	}
	if s.Deps.Store != nil && s.Deps.Store.IsPostgres() && data.LoadErr != nil {
		snap := BuildSnapshot(Data{LoadedAt: time.Now(), LoadErr: data.LoadErr}, nil)
		snap.Storage = "postgres"
		snap.Executions = []SnapExecution{}
		return snap
	}
	s.freshRanks(&data) // moves written while the load ran
	if data.LoadErr == nil || len(data.Projects) > 0 {
		s.last.set(data) // never replace good data with a failed read
	}
	s.last.finished(data.LoadErr)
	if s.Unread != nil && (s.unread == nil || time.Since(s.unreadAt) >= s.UnreadEvery) {
		counts := map[string]int{}
		for _, role := range MailRoles(data) {
			// Bounded: after a restart the mail server is busy with every
			// agent's wake-up, and the app's start screen waits on this
			// snapshot. A slow role keeps its last count until the next try.
			c, cancel := context.WithTimeout(ctx, unreadTimeout)
			n, err := s.Unread(c, role)
			cancel()
			if err == nil {
				counts[role] = n
			} else if old, ok := s.unread[role]; ok {
				counts[role] = old
			}
		}
		s.unread, s.unreadAt = counts, time.Now()
	}
	var live *SnapLive
	liveRead := s.Live
	var databaseChats map[string]ChatState
	if s.Deps.Store != nil && s.Deps.Store.IsPostgres() {
		databaseChats = map[string]ChatState{}
		alive, native, known, e := s.Deps.Store.RuntimePresence(ctx)
		if e != nil {
			return Snapshot{Storage: "postgres", Executions: []SnapExecution{}, Degraded: &SnapDegraded{Reason: "Current runtime state is unavailable"}}
		}
		for _, p := range native {
			databaseChats[p.Role] = ChatState{State: p.State, Detail: p.Detail}
		}
		facts, err := s.Deps.Store.ObservedExecutions(ctx)
		if err != nil {
			return Snapshot{Storage: "postgres", Executions: []SnapExecution{}, Degraded: &SnapDegraded{Reason: "Current execution evidence is unavailable"}}
		}
		data.ObservedExecutions = facts
		data.Objectives = markObservedExecutionOwners(data.Objectives, facts)
		maskObservedAgentStates(&data)

		liveRead = func() ([]string, time.Time, bool) { return alive, time.Now(), known }
	}
	if data.ObservedExecutions != nil {
		// PostgreSQL counts only fresh observations, never the legacy grace that
		// treated a newly inserted claim as an already running process.
		alive := []string{}
		for _, fact := range data.ObservedExecutions {
			if fact.Known && fact.Alive {
				alive = append(alive, fact.Role)
			}
		}
		live = NewSnapLive(alive, time.Now().Unix(), nil)
		data.Operators = LiveRuns(data.Operators, live.operators())
	} else if liveRead != nil {
		if alive, at, ok := liveRead(); ok {
			live = NewSnapLive(alive, at.Unix(), data.Operators)
			data.Objectives = MarkLiveOwners(data.Objectives, live.operators())
			if s.Reported != nil {
				live.Reported = s.Reported()
				data.Objectives = MarkReported(data.Objectives, live.Reported, live.operators())
			}
			data.Operators = LiveRuns(data.Operators, live.operators())
		}
	}
	snap := BuildSnapshot(data, s.unread)
	RefreshWorkflowHeads(&snap, s.PRHeads)
	if s.PRStates != nil {
		snap.PRStates = s.PRStates()
	}
	if s.Deps.Store != nil {
		snap.Answers = map[string]int{}
		for _, p := range data.Projects {
			pitches, err := s.Deps.Store.ListPitches(ctx, p.ID, "")
			if err != nil {
				continue
			}
			for _, pitch := range pitches {
				key := p.Name + ":" + pitch.BeadID
				if _, ok := snap.Answers[key]; !ok {
					snap.Answers[key] = 0 // successfully read, no answered version yet
				}
				if pitch.AnsweredAt > 0 {
					if pitch.Version > snap.Answers[key] {
						snap.Answers[key] = pitch.Version
					}
				}
			}
		}
	}
	snap.Live = live
	if s.Deps.Store != nil && s.Deps.Store.IsPostgres() && liveRead != nil && live == nil {
		snap.Degraded = &SnapDegraded{Reason: "Operator process activity could not be verified"}
	}
	if s.Health != nil {
		snap.Health = s.Health()
	}
	var projectErr *projectReadError
	snap.PreserveVersion = errors.As(data.LoadErr, &projectErr)
	snap.KeptLastGood = data.KeptLastGood
	snap.ProjectTimes = data.ProjectTimes
	if s.ReviewFacts != nil {
		if facts := s.ReviewFacts(); len(facts) > 0 {
			for i := range snap.Workstreams {
				markReviewFacts(snap.Workstreams[i].Steps, facts)
			}
		}
	}
	if s.WaitsChecked != nil {
		if t := s.WaitsChecked(); !t.IsZero() {
			for i := range snap.Workstreams {
				markWaitsChecked(snap.Workstreams[i].Steps, t.Unix())
			}
		}
	}
	if s.PitchAsked != nil {
		if asked := s.PitchAsked(); len(asked) > 0 {
			for i := range snap.NoPitch {
				snap.NoPitch[i].AskedAt = asked[snap.NoPitch[i].Project+":"+snap.NoPitch[i].BeadID]
			}
		}
	}
	now := time.Now().Unix()
	currentChats := map[string]ChatState{}
	if s.Chats != nil || databaseChats != nil {
		if s.chatSince == nil {
			s.chatSince = map[string]SnapAgent{}
		}
		if databaseChats != nil {
			currentChats = databaseChats
		} else {
			currentChats = s.Chats()
		}
		if s.Deps.Store != nil && s.Deps.Store.IsPostgres() {
			for role, a := range snap.Agents {
				if role == "commander" || strings.HasPrefix(role, "manager-") {
					if _, ok := currentChats[role]; !ok {
						a.State = store.AgentIdle
						a.Detail = "Process is not running"
						snap.Agents[role] = a
					}
				}
			}
		}
		overlayChats(&snap, currentChats, s.chatSince, now)
		// The predicate reads the agent state the board shows: a Manager the overlay has mid-turn
		// holds what it owns, as on the board's line and in StalledWork.
		markOverlaid(&snap, data.Objectives, data.LoadedAt)
	}
	if s.Deps.Store != nil && s.Deps.Store.IsPostgres() {
		snap.Storage = "postgres"
		snap.Executions = executionOf(snap, data, currentChats)
		if data.ObservedExecutions != nil {
			snap.ExecutionChecks = unverifiedExecutionOf(snap, data)
			applyObservedWorking(&snap)
		}
	}
	d := Board(snap, now)
	applyExecutionCounts(&snap, &d)
	if err := applyClassifications(ctx, s.Deps.Store, &snap, &d); err != nil {
		log.Printf("status classifier: evidence read unavailable")
	}
	applyDescriptions(s.Describe, &snap, &d)
	if err := s.since.fill(ctx, s.Deps.Store, &d, now, data.LoadErr != nil); err != nil {
		log.Printf("livedata: %v", err)
	}
	for i := range snap.NeedsYou {
		snap.NeedsYou[i].TrackLetter = d.Asks[i].TrackLetter
	}
	snap.Display = &d
	if s.OnSnapshot != nil {
		s.OnSnapshot(&snap)
	}
	snap.Phases = data.Phases
	snap.Phases.Build = time.Since(start)
	return snap
}

// markWaitsChecked sets "last checked" on every step with a recorded wait.
// ReviewFact is a step's review waits as GitHub has them now.
type ReviewFact struct {
	// Acted: a reviewer it waits on has reviewed since the wait began, so the
	// next move is the agents', not the reviewer's.
	Acted bool `json:"acted,omitempty"`
	// NoPR: the wait names no PR (no pr:<n> label), so nothing can clear it.
	NoPR bool   `json:"no_pr,omitempty"`
	Text string `json:"text"` // "reviewer-a approved #4109" / "waiting on reviewer-a (#4101)"
}

func markReviewFacts(steps []SnapStep, facts map[string]ReviewFact) {
	for i := range steps {
		if f, ok := facts[steps[i].BeadID]; ok {
			steps[i].ReviewFact = &f
		}
		markReviewFacts(steps[i].Children, facts)
	}
}

func markWaitsChecked(steps []SnapStep, at int64) {
	for i := range steps {
		if steps[i].Waiting != "" && at > steps[i].WaitChecked {
			steps[i].WaitChecked = at
		}
		markWaitsChecked(steps[i].Children, at)
	}
}

// watchOf groups the running Operators (data.Operators holds only those) by
// their workstream's Manager.
func watchOf(data Data) map[string]SnapWatch {
	out := map[string]SnapWatch{}
	parents := data.Parents()
	byID := rowsByID(data.Objectives)
	managerOf := func(bead string) string {
		return store.WorkstreamManagerRole(ownerByParents(byID, parents, bead))
	}
	for _, op := range data.Operators {
		manager := managerOf(op.TaskBeadID)
		w := out[manager]
		c := data.Checkins[manager]
		w.CheckedAt, w.CheckinAt = c.Checked, c.Due
		o := SnapWatchedOp{Role: op.AgentName, BeadID: op.TaskBeadID, Since: op.SpawnTime.Unix()}
		if !op.ActiveAt.IsZero() {
			o.ActiveAt = op.ActiveAt.Unix()
		}
		w.Operators = append(w.Operators, o)
		out[manager] = w
	}
	for _, e := range data.Finished {
		manager := managerOf(e.TaskBeadID.String)
		w := out[manager]
		if w.Operators == nil {
			w.Operators = []SnapWatchedOp{}
		}
		w.Finished = append(w.Finished, SnapFinishedOp{
			Role: e.AgentName, BeadID: e.TaskBeadID.String, At: e.OccurredAt,
			Died: e.EventType == store.EventOperatorDied, Report: e.Reason.String,
		})
		out[manager] = w
	}
	if c, ok := data.Checkins["commander"]; ok {
		w := SnapWatch{CheckedAt: c.Checked, CheckinAt: c.Due, Operators: []SnapWatchedOp{}}
		for _, i := range data.Issues {
			if i.Active {
				w.Issues = append(w.Issues, SnapIssue{Workstream: i.WS, Text: i.Text, Since: i.Since})
			}
		}
		out["commander"] = w
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// markBroken sets Broken on the step of each violation.
func markBroken(workstreams []SnapWorkstream, broken []Violation) {
	if len(broken) == 0 {
		return
	}
	ids := make(map[string]bool, len(broken))
	for _, v := range broken {
		ids[v.BeadID] = true
	}
	var walk func(steps []SnapStep)
	walk = func(steps []SnapStep) {
		for i := range steps {
			steps[i].Broken = ids[steps[i].BeadID]
			walk(steps[i].Children)
		}
	}
	for i := range workstreams {
		walk(workstreams[i].Steps)
	}
}

// hasGateBelow: a gate bead or a stage gate anywhere under n (a track's own gate chain).
func hasGateBelow(n *StepNode) bool {
	for _, c := range n.Children {
		if c.Row.Gate || len(c.Row.Gates) > 0 || hasGateBelow(c) {
			return true
		}
	}
	return false
}
