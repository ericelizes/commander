// Package wsmgr gives every workstream (top-level objective) its own Manager:
// a Claude chat session in its own tmux window with its own mail role
// (manager-<workstream bead id>). It prepares the session, prompt, mail
// registration and state.db row, and opens or focuses the window.
package wsmgr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/layout"
	"github.com/example-user/commander/internal/mailstate"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tui"
	promptassets "github.com/example-user/commander/prompts"
)

// Manager wires the pieces. Zero-value fields marked "test seam" default to the
// real implementation.
type Manager struct {
	Store *store.Store
	Beads beads.Client
	Reg   agentmail.AgentRegistrar
	AmKey string // fleet am project key (the commander checkout)
	Dir   *layout.Director

	// Numbered lists the open workstreams with their ranks (test seam).
	Numbered func(ctx context.Context) ([]tui.NumberedWorkstream, error)
	// Fresh lists the workstreams from br itself, bypassing any cached
	// snapshot (test seam). Consulted when Numbered does not know a workstream:
	// cmdrd's snapshot lags one created seconds ago.
	Fresh func(ctx context.Context) ([]tui.NumberedWorkstream, error)
	// Settle waits for a freshly started Claude to accept input (test seam).
	Settle time.Duration
	// Native runs Managers as native app chats through cmdrd. Nil (or an
	// error from Start) falls back to a tmux terminal window.
	Native NativeChats
}

// NativeChats is cmdrd's chat engine as seen by workstream Managers.
type NativeChats interface {
	// Start runs role's chat if it is not running (a no-op when it is).
	Start(ctx context.Context, role string) error
	// Fresh reports that role has never run natively (so a stopped or crashed
	// chat is not restarted behind the user's back by EnsureNative).
	Fresh(role string) bool
	// Send puts text in a running role's chat.
	Send(ctx context.Context, role, text string) error
}

// ErrSessionHeld marks a Start refused because something else (a terminal)
// owns the session; the Manager stays where it is.
var ErrSessionHeld = errors.New("session held by another process")

// Target is one workstream.
type Target struct {
	Project store.Project
	BeadID  string
	Title   string
	Rank    int
	// Parent is the owner above a nested workstream ("" for a top-level one).
	Parent string
	// Children are the nested workstreams directly under it (preamble only).
	Children []string
}

// Role is the workstream Manager's mail role and Claude agent name.
func Role(beadID string) string { return store.WorkstreamManagerRole(beadID) }

// Owner is the workstream whose Manager owns beadID, by real parent-child
// edges: the nearest open kind:workstream bead (beads.Owner), the top-level one
// while the project's nested Managers are off. A promoted step is its own
// workstream. For display and mail routing; actions use StrictOwner.
func Owner(ctx context.Context, c beads.Client, dir, beadID string) string {
	return beads.Owner(ctx, c, dir, nil, nil, beadID)
}

func sessionFilePath(projectPath, beadID string) string {
	return filepath.Join(projectPath, ".commander", "workstreams", agentrt.SessionFileName(beadID, agentrt.Claude))
}

// Preamble is prepended to the Manager role prompt.
func Preamble(t Target) string {
	pre := fmt.Sprintf(`# Your workstream

You are the Manager of ONE workstream, not the whole project.

- Workstream: %s (bead %s) in project %s (%s).
- Your mail role: %s. Commander is "commander"; your Operators are operator-<branch>.
- Your scope is every bead whose parent chain leads to %s, whatever its id prefix: tracks and moved beads often carry their own prefix and are IN scope. Check with `+"`bd show <id>`"+` (its PARENT section) or `+"`cmdr internal-workstreams`"+`; list your steps with `+"`bd list --label kind:objective --status all --json`"+` in the project dir.
- Other workstreams in this project belong to other Managers. Read them for context, but change only beads under %s by parentage.
- Wherever the instructions below say "<workstream-id>" or "<project>", use %s and %s.`,
		t.Title, t.BeadID, t.Project.Name, t.Project.Path,
		Role(t.BeadID), t.BeadID, t.BeadID, t.BeadID, t.Project.Name)
	if t.Parent == "" && len(t.Children) == 0 {
		return pre
	}
	pre += "\n\n## Nested Managers\n\n- You own the beads whose NEAREST workstream is " + t.BeadID + ": a nested workstream under you has its own Manager (manager-<its bead>), and its beads and Operators are that Manager's, not yours."
	if t.Parent != "" {
		pre += fmt.Sprintf("\n- You are a nested Manager. Your parent is %s (workstream %s): mail it on milestones (gate passed, PR merged, done) and when a sibling workstream blocks you. Never write beads under a sibling workstream.", Role(t.Parent), t.Parent)
	}
	if len(t.Children) > 0 {
		var roles []string
		for _, c := range t.Children {
			roles = append(roles, Role(c))
		}
		pre += "\n- Nested workstreams under you, each with its own Manager: " + strings.Join(roles, ", ") + ". Dispatch to them by mail and read their rows with `cmdr board --role " + Role(t.BeadID) + "`; do not spawn Operators under them."
	}
	return pre
}

// Prepare makes everything a workstream Manager needs except its window:
// session id, prompt file, mail registration and the state.db row. A nested
// Manager also gets a mail cursor before anything can mail it, so its first
// mail is news and never history held by the no-cursor rule.
func (m *Manager) Prepare(ctx context.Context, t Target) (layout.WorkstreamWindow, error) {
	if active,e:=m.Store.ActiveOrchestration(ctx);e!=nil{return layout.WorkstreamWindow{},e}else if active{
		j,e:=m.Store.Jurisdiction(ctx,t.Project.ID,t.BeadID);if e!=nil{return layout.WorkstreamWindow{},e};if j.CommanderID!=Role(t.BeadID){return layout.WorkstreamWindow{},fmt.Errorf("workstream planning belongs to %s; workstreams do not create permanent agent roles",j.CommanderID)}
	}
	parent, err := m.validateOwner(ctx, t)
	if err != nil {
		return layout.WorkstreamWindow{}, err
	}
	t.Parent = parent
	if beads.NestedManagersOnCtx(ctx, t.Project.Path) {
		t.Children = m.nestedChildren(ctx, t)
	}
	role := Role(t.BeadID)

	sessionFile := sessionFilePath(t.Project.Path, t.BeadID)
	if row, err := m.Store.GetWorkstreamManager(ctx, t.Project.ID, t.BeadID); err != nil {
		return layout.WorkstreamWindow{}, err
	} else if row != nil && row.SessionFile != "" {
		sessionFile = row.SessionFile // adopted Managers keep their original session file
	}
	sessionID, err := readOrMintSession(sessionFile)
	if err != nil {
		return layout.WorkstreamWindow{}, err
	}

	if err := promptassets.EnsureClaudeAgentWithPreamble(promptassets.RoleManager, role, Preamble(t)); err != nil {
		return layout.WorkstreamWindow{}, fmt.Errorf("manager prompt for %s: %w", t.BeadID, err)
	}
	if err := m.ensureMailRole(ctx, role); err != nil {
		return layout.WorkstreamWindow{}, err
	}
	if t.Parent != "" {
		if err := mailstate.StampFresh(role, time.Now()); err != nil {
			return layout.WorkstreamWindow{}, fmt.Errorf("mail cursor for %s: %w", role, err)
		}
	}
	if err := m.Store.UpsertWorkstreamManager(ctx, store.WorkstreamManager{
		ProjectID: t.Project.ID, WorkstreamBeadID: t.BeadID, AmRole: role, SessionFile: sessionFile,
	}); err != nil {
		return layout.WorkstreamWindow{}, err
	}
	return layout.WorkstreamWindow{
		ProjectName: t.Project.Name, ProjectPath: t.Project.Path,
		BeadID: t.BeadID, Title: t.Title, Rank: t.Rank,
		AgentName: role, SessionID: sessionID,
	}, nil
}

// nestedChildren are the open nested workstreams directly under t (best
// effort: the preamble only names them).
func (m *Manager) nestedChildren(ctx context.Context, t Target) []string {
	kids, err := beads.OpenChildren(ctx, m.Beads, t.Project.Path, t.BeadID)
	if err != nil {
		return nil
	}
	var out []string
	for _, k := range kids {
		if beads.OwnsWork(k) {
			out = append(out, k.ID)
		}
	}
	return out
}

func (m *Manager) ensureMailRole(ctx context.Context, role string) error {
	if name, err := m.Store.GetAmAgent(ctx, m.AmKey, role); err != nil || name != "" {
		return err
	}
	if _, err := m.Reg.EnsureProject(ctx, m.AmKey); err != nil {
		return fmt.Errorf("ensure am project: %w", err)
	}
	name, err := m.Reg.RegisterAgent(ctx, m.AmKey, "claude-code", "sonnet", "agent for "+role)
	if err != nil {
		return fmt.Errorf("register mail role %s: %w", role, err)
	}
	return m.Store.UpsertAmAgent(ctx, m.AmKey, role, name)
}

func readOrMintSession(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	id := agentrt.NewClaudeSessionID()
	if err := os.WriteFile(path, []byte(id+"\n"), 0o644); err != nil {
		return "", err
	}
	return id, nil
}

// Workstreams lists every open workstream, in rank order.
func (m *Manager) Workstreams(ctx context.Context) ([]Target, error) {
	numbered := m.Numbered
	if numbered == nil {
		numbered = tui.ListNumberedWorkstreams
	}
	return m.targets(ctx, numbered)
}

// Owners lists every workstream that has a Manager of its own: the top-level
// workstreams (Workstreams) and, in a project whose nested-Managers switch is
// on, each open kind:workstream bead under one of them. A nested target keeps
// its root's Rank; Parent names the owner above it.
func (m *Manager) Owners(ctx context.Context) ([]Target, error) {
	top, err := m.Workstreams(ctx)
	if err != nil {
		return nil, err
	}
	return m.withNested(ctx, top)
}

func (m *Manager) withNested(ctx context.Context, top []Target) ([]Target, error) {
	out := slices.Clone(top)
	byProject := map[int64][]Target{}
	var order []int64
	for _, t := range top {
		if !beads.NestedManagersOnCtx(ctx, t.Project.Path) {
			continue
		}
		if _, ok := byProject[t.Project.ID]; !ok {
			order = append(order, t.Project.ID)
		}
		byProject[t.Project.ID] = append(byProject[t.Project.ID], t)
	}
	for _, pid := range order {
		roots := byProject[pid]
		p := roots[0].Project
		list, err := m.Beads.List(ctx, p.Path, beads.ListOptions{Labels: []string{beads.WorkstreamLabel}, Status: "all"})
		if err != nil {
			return out, fmt.Errorf("nested workstreams of %s: %w", p.Name, err)
		}
		parents := beads.Parents{}
		for _, b := range list {
			if par := listedParent(b); par != "" {
				parents[b.ID] = par
			}
		}
		known, err := fillParents(ctx, m.Beads, p.Path, list, parents)
		if err != nil {
			return out, fmt.Errorf("nested workstreams of %s: %w", p.Name, err)
		}
		out = append(out, nestedTargets(p, roots, list, known, parents)...)
	}
	return out, nil
}

func listedParent(b beads.Bead) string {
	if b.Parent != "" {
		return b.Parent
	}
	return b.ParentID
}

// nestedTargets picks the open nested workstreams under roots from list (every
// kind:workstream bead of the project) that own work by beads.ChainOwners,
// parents before children.
func nestedTargets(p store.Project, roots []Target, list []beads.Bead, known map[string]beads.Bead, parents beads.Parents) []Target {
	rank := map[string]int{}
	for _, r := range roots {
		rank[r.BeadID] = r.Rank
	}
	return nestedOwned(list, known, parents, func(b beads.Bead, top, parent string) (Target, bool) {
		r, ok := rank[top]
		return Target{Project: p, BeadID: b.ID, Title: b.Title, Rank: r, Parent: parent}, ok
	})
}

// chainOf is id's chain (id first) by parents, ending at a bead with no known
// parent; ok=false when a parent is not in parents yet or the walk loops.
func chainOf(id string, parents beads.Parents) (chain []string, ok bool) {
	for cur := id; len(chain) < 64; {
		chain = append(chain, cur)
		par, known := parents[cur]
		if !known {
			return chain, false
		}
		if par == "" {
			return chain, true
		}
		cur = par
	}
	return chain, false
}

// fillParents reads (fresh) the parent of every bead the chains of list's
// owning workstreams pass through that parents does not hold yet, plain steps
// included, so a nested workstream under a step is judged like beads.Owner
// judges it. A bead the read does not return counts as a top. It answers every
// bead it knows (list and the plain beads between), for nestedOwned.
func fillParents(ctx context.Context, bc beads.Client, dir string, list []beads.Bead, parents beads.Parents) (map[string]beads.Bead, error) {
	if err := readParents(ctx, bc, dir, list, parents); err != nil {
		return nil, err
	}
	// The plain beads those chains pass through, for their status: a closed one
	// puts what is under it out of play (beads.ChainOwners).
	listed := map[string]bool{}
	for _, b := range list {
		listed[b.ID] = true
	}
	var between []string
	for _, b := range list {
		if !beads.OwnsWork(b) {
			continue
		}
		chain, _ := chainOf(b.ID, parents)
		for _, id := range chain[1:] {
			if !listed[id] && !slices.Contains(between, id) {
				between = append(between, id)
			}
		}
	}
	out := map[string]beads.Bead{}
	for _, b := range list {
		out[b.ID] = b
	}
	if len(between) == 0 {
		return out, nil
	}
	slices.Sort(between)
	got, err := bc.List(ctx, dir, beads.ListOptions{IDs: between, Status: "all"})
	if err != nil {
		return nil, err
	}
	for _, b := range got {
		out[b.ID] = b
	}
	return out, nil
}

func readParents(ctx context.Context, bc beads.Client, dir string, list []beads.Bead, parents beads.Parents) error {
	for round := 0; round < 64; round++ {
		var missing []string
		seen := map[string]bool{}
		for _, b := range list {
			if !beads.OwnsWork(b) {
				continue
			}
			chain, ok := chainOf(b.ID, parents)
			if last := chain[len(chain)-1]; !ok && !seen[last] {
				if _, known := parents[last]; !known {
					seen[last] = true
					missing = append(missing, last)
				}
			}
		}
		if len(missing) == 0 {
			return nil
		}
		slices.Sort(missing)
		ds, err := beads.FreshDetails(ctx, bc, dir, missing)
		if err != nil {
			return err
		}
		for _, id := range missing {
			parents[id] = ds[id].Parent
		}
	}
	return nil
}

// nestedOwned is the nested owners in list (kind:workstream beads) whose full
// chain by parents reaches a top accept takes, and that beads.ChainOwners
// counts as owners: the rule beads.Owner routes by, so every bead routed to is
// registered. Ordered parents first, then by bead id, so the order never
// depends on the list's.
func nestedOwned(list []beads.Bead, byID map[string]beads.Bead, parents beads.Parents, accept func(b beads.Bead, top, parent string) (Target, bool)) []Target {
	type found struct {
		t     Target
		depth int
	}
	var out []found
	for _, b := range list {
		if parents[b.ID] == "" || !beads.OwnsWork(b) {
			continue
		}
		chain, ok := chainOf(b.ID, parents)
		if !ok || !slices.Contains(beads.ChainOwners(chain, byID), b.ID) {
			continue
		}
		// The parent is the next owner above b, else the chain's top: never a
		// plain step in between (it has no Manager).
		top := chain[len(chain)-1]
		parent := top
		owners := beads.ChainOwners(chain, byID)
		// ChainOwners is nearest first.
		if i := slices.Index(owners, b.ID); i >= 0 && i+1 < len(owners) {
			parent = owners[i+1]
		}
		if t, ok := accept(b, top, parent); ok {
			out = append(out, found{t, len(chain) - 1})
		}
	}
	slices.SortFunc(out, func(a, b found) int {
		if a.depth != b.depth {
			return a.depth - b.depth
		}
		return strings.Compare(a.t.BeadID, b.t.BeadID)
	})
	ts := make([]Target, len(out))
	for i, f := range out {
		ts[i] = f.t
	}
	return ts
}

// find returns the first workstream match accepts. A miss in the possibly
// stale list is re-checked against a fresh read before it counts as absent.
func (m *Manager) find(ctx context.Context, match func(Target) bool) (*Target, error) {
	targets, err := m.Owners(ctx)
	if err != nil {
		return nil, err
	}
	if t := firstTarget(targets, match); t != nil {
		return t, nil
	}
	fresh := m.Fresh
	if fresh == nil {
		fresh = tui.ListNumberedWorkstreamsFresh
	}
	if targets, err = m.targets(ctx, fresh); err != nil {
		return nil, err
	}
	if targets, err = m.withNested(ctx, targets); err != nil {
		return nil, err
	}
	return firstTarget(targets, match), nil
}

func firstTarget(targets []Target, match func(Target) bool) *Target {
	for i := range targets {
		if match(targets[i]) {
			return &targets[i]
		}
	}
	return nil
}

func (m *Manager) targets(ctx context.Context, numbered func(context.Context) ([]tui.NumberedWorkstream, error)) ([]Target, error) {
	rows, err := numbered(ctx)
	if err != nil {
		return nil, err
	}
	projects, err := m.Store.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]store.Project, len(projects))
	for _, p := range projects {
		byName[p.Name] = p
	}
	out := make([]Target, 0, len(rows))
	for _, r := range rows {
		if p, ok := byName[r.Project]; ok {
			out = append(out, Target{Project: p, BeadID: r.BeadID, Title: r.Title, Rank: r.N})
		}
	}
	return out, nil
}

// PrepareAll prepares every open workstream's Manager (start/restart). A
// target that fails (a nested display row, an unreadable ancestry) is skipped:
// the windows of every other workstream come back with the joined errors, so
// one bad row never costs the legitimate Managers their windows.
func (m *Manager) PrepareAll(ctx context.Context) ([]layout.WorkstreamWindow, error) {
	if active,e:=m.Store.ActiveOrchestration(ctx);e!=nil{return nil,e}else if active{return []layout.WorkstreamWindow{},nil}
	targets, err := m.Workstreams(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]layout.WorkstreamWindow, 0, len(targets))
	var skipped []error
	for _, t := range targets {
		w, err := m.Prepare(ctx, t)
		if err != nil {
			skipped = append(skipped, fmt.Errorf("skipped %s: %w", t.BeadID, err))
			continue
		}
		out = append(out, w)
	}
	return out, errors.Join(skipped...)
}

// OpenOptions controls Open.
type OpenOptions struct {
	Focus  bool   // select the Manager's window
	StepID string // when set, point the Manager at this step
}

// OpenResult reports what Open did.
type OpenResult struct {
	PlanningCommander string `json:"planning_commander,omitempty"`
	Project string `json:"project"`
	BeadID  string `json:"workstream_bead_id"`
	Role    string `json:"manager_role"`
	Rank    int    `json:"rank"`
	Created bool   `json:"created"`
	Pane    string `json:"pane"`
	Native  bool   `json:"native,omitempty"` // runs as a native app chat, no tmux window
}

// Open ensures the Manager of the workstream containing beadID exists and has
// a window, optionally focusing it and pointing it at a step.
func (m *Manager) Open(ctx context.Context, projectName, beadID string, opt OpenOptions) (*OpenResult, error) {
	p, err := m.Store.GetProjectByName(ctx, projectName)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("unknown project %s", projectName)
	}
	if active,e:=m.Store.ActiveOrchestration(ctx);e!=nil{return nil,e}else if active{
		focus:=beadID
		if opt.StepID!=""{
			var inside bool
			if e=m.Store.DB().QueryRowContext(ctx,`WITH RECURSIVE ancestry(id,parent_id,path) AS (SELECT id,parent_id,ARRAY[id] FROM bead_tasks WHERE project_id=? AND id=? AND deleted_at IS NULL UNION ALL SELECT b.id,b.parent_id,a.path||b.id FROM bead_tasks b JOIN ancestry a ON a.parent_id=b.id WHERE b.project_id=? AND b.deleted_at IS NULL AND NOT b.id=ANY(a.path)) SELECT EXISTS(SELECT 1 FROM ancestry WHERE id=?)`,p.ID,opt.StepID,p.ID,beadID).Scan(&inside);e!=nil{return nil,e}
			if !inside{return nil,fmt.Errorf("focus bead %s is outside the requested workstream",opt.StepID)};focus=opt.StepID
		}
		j,e:=m.Store.Jurisdiction(ctx,p.ID,focus);if e!=nil{return nil,e}
		if m.Native==nil{return nil,fmt.Errorf("open planning conversation %s through the native chat interface",j.CommanderID)}
		if e=m.Native.Start(ctx,j.CommanderID);e!=nil{return nil,e}
		if opt.StepID!=""{if e=m.Native.Send(ctx,j.CommanderID,"The user opened bead "+opt.StepID+". Read its current goal, jurisdiction and recorded state before answering.");e!=nil{return nil,e}}
		return &OpenResult{Project:projectName,BeadID:beadID,Role:j.CommanderID,PlanningCommander:j.CommanderID,Native:true},nil
	}
	// The nearest open workstream owns the bead (its root while the project's
	// nested switch is off).
	root, err := StrictOwner(ctx, m.Beads, p.Path, beadID)
	if err != nil {
		return nil, err
	}
	// A workstream created seconds ago may not be in the snapshot yet: find re-reads br.
	target, err := m.find(ctx, func(t Target) bool { return t.BeadID == root && t.Project.Name == projectName })
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, fmt.Errorf("%s is not an open workstream in %s", root, projectName)
	}
	w, err := m.Prepare(ctx, *target)
	if err != nil {
		return nil, err
	}
	var nativeErr error
	if m.Native != nil {
		// cmdrd can run this Manager itself: a terminal pane only appears
		// when the user pops the chat out (or cmdrd cannot start it).
		// A session held by a terminal, or cmdrd being down, reuses the window below.
		if err := m.Native.Start(ctx, w.AgentName); err == nil {
			if opt.StepID != "" {
				if err := m.Native.Send(ctx, w.AgentName, m.pointerPrompt(ctx, *target, opt.StepID)); err != nil {
					return nil, err
				}
			}
			return &OpenResult{Project: projectName, BeadID: w.BeadID, Role: w.AgentName, Rank: w.Rank, Native: true}, nil
		} else if noUsage(err) {
			// A terminal window would start it on an account that is out: say so instead.
			return nil, fmt.Errorf("start %s: %w", w.AgentName, err)
		} else {
			nativeErr = err
		}
	}
	pane, created, err := m.Dir.EnsureWorkstreamWindow(ctx, w)
	if err != nil {
		if nativeErr != nil {
			// The tmux error alone hides why the chat engine refused.
			return nil, fmt.Errorf("native start of %s failed: %v (terminal fallback: %v)", w.AgentName, nativeErr, err)
		}
		return nil, err
	}
	if created {
		// Number and name the new window with the rest ("#<rank>", Commander = 0).
		targets, _ := m.Workstreams(ctx)
		names := make(map[string]layout.WorkstreamWindow, len(targets)+1)
		for _, t := range targets {
			names[t.BeadID] = layout.WorkstreamWindow{Rank: t.Rank, Title: t.Title}
		}
		names[target.BeadID] = layout.WorkstreamWindow{Rank: target.Rank, Title: target.Title}
		_ = m.Dir.SyncWorkstreamNames(ctx, names)
	}
	if opt.Focus {
		if err := m.Dir.FocusWorkstream(ctx, w.BeadID); err != nil {
			return nil, err
		}
	}
	if opt.StepID != "" {
		if created && m.Settle > 0 {
			time.Sleep(m.Settle) // a fresh Claude can't take input immediately
		}
		if err := m.Dir.SendToPane(ctx, pane, m.pointerPrompt(ctx, *target, opt.StepID)); err != nil {
			return nil, err
		}
	}
	return &OpenResult{Project: projectName, BeadID: w.BeadID, Role: w.AgentName, Rank: w.Rank, Created: created, Pane: pane}, nil
}

func (m *Manager) pointerPrompt(ctx context.Context, t Target, stepID string) string {
	title, state, owner := stepID, "", ""
	if all, err := m.Beads.List(ctx, t.Project.Path, beads.ListOptions{Labels: []string{"kind:objective"}, Status: "all"}); err == nil {
		for _, b := range all {
			if b.ID == stepID {
				title = b.Title
				state, owner = beads.StepState(b.Status, b.Labels)
			}
		}
	}
	detail := state
	if owner != "" {
		detail += ", owner " + owner
	}
	if detail != "" {
		detail = " (" + detail + ")"
	}
	if stepID == t.BeadID {
		return fmt.Sprintf("the user opened your workstream %q. Give him a short status and say what you will do next.", t.Title)
	}
	return fmt.Sprintf("the user opened step %s %q%s in %q. Give him status on it and pick it up.", stepID, title, detail, t.Title)
}

// EnsureNative prepares every open workstream's Manager and starts its native
// chat, so a new workstream's Manager exists before anyone writes to it.
// Managers held by a terminal, and ones that ran before and stopped, are left alone. It returns the first hard error
// after trying every workstream.
func (m *Manager) EnsureNative(ctx context.Context) error {
	if m.Native == nil {
		return nil
	}
	if active,e:=m.Store.ActiveOrchestration(ctx);e!=nil{return e}else if active{return nil}
	targets, err := m.Owners(ctx)
	if err != nil {
		return err
	}
	stand := standIns(targets)
	var first error
	for _, t := range targets {
		role := Role(t.BeadID)
		if !m.Native.Fresh(role) {
			continue
		}
		// A parent whose only open child is a nested workstream: the child stands
		// in its place, so only mail or an explicit open starts the parent.
		if stand[t.BeadID] {
			continue
		}
		if err := m.validateTarget(ctx, t); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		// The chat engine's start draws a new Manager's runtime and steers it to
		// an account with usage left (chat.Engine.start), on every start path.
		if err := m.EnsureTarget(ctx, t); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// noUsage reports a native start refused because no account has usage left
// (chat.ErrNoUsage, also as the text of cmdrd's HTTP answer).
func noUsage(err error) bool {
	return errors.Is(err, chat.ErrNoUsage) || strings.Contains(err.Error(), chat.ErrNoUsage.Error())
}

// standIns are the owners with exactly one nested owner under them.
func standIns(targets []Target) map[string]bool {
	kids := map[string]int{}
	for _, t := range targets {
		if t.Parent != "" {
			kids[t.Parent]++
		}
	}
	out := map[string]bool{}
	for id, n := range kids {
		if n == 1 {
			out[id] = true
		}
	}
	return out
}

// EnsureTarget prepares one Manager and starts its native chat.
func (m *Manager) EnsureTarget(ctx context.Context, t Target) error {
	w, err := m.Prepare(ctx, t)
	if err != nil {
		return err
	}
	if err := m.Native.Start(ctx, w.AgentName); err != nil {
		// Held elsewhere (a terminal the user has open) is not a failure to
		// start, but the caller needs to know the Manager is not running here.
		return fmt.Errorf("start %s: %w", w.AgentName, err)
	}
	return nil
}

// EnsureRole wakes one registered owner without a full-fleet discovery read.
// Explicit Open still uses find's fresh fallback for newly created workstreams.
func (m *Manager) EnsureRole(ctx context.Context, role string) error {
	row, err := m.Store.GetWorkstreamManagerByRole(ctx, role)
	if err != nil {
		return err
	}
	if row != nil {
		if active,e:=m.Store.ActiveOrchestration(ctx);e!=nil{return e}else if active{j,e:=m.Store.Jurisdiction(ctx,row.ProjectID,row.WorkstreamBeadID);if e!=nil{return e};if j.CommanderID!=role{return fmt.Errorf("%w: planning jurisdiction now belongs to %s",ErrInvalidRoute,j.CommanderID)}}
		if Role(row.WorkstreamBeadID) != role {
			return fmt.Errorf("%w: inconsistent registration for %q", ErrInvalidRoute, role)
		}
		p, err := m.Store.GetProject(ctx, row.ProjectID)
		if err != nil {
			return err
		}
		if p == nil {
			return fmt.Errorf("%w: missing project for %q", ErrInvalidRoute, role)
		}
		bead, err := beads.FreshShow(ctx, m.Beads, p.Path, row.WorkstreamBeadID)
		if err != nil {
			return err
		}
		if bead == nil || beads.Superseded(*bead) {
			return fmt.Errorf("%w: missing or retired workstream for %q", ErrInvalidRoute, role)
		}
		ranks, err := m.Store.ListWorkstreamRanks(ctx)
		if err != nil {
			return err
		}
		return m.EnsureTarget(ctx, Target{Project: *p, BeadID: row.WorkstreamBeadID, Title: bead.Title, Rank: ranks[store.WorkstreamKey{ProjectID: p.ID, BeadID: row.WorkstreamBeadID}]})
	}
	if m.Numbered != nil {
		targets, err := m.targets(ctx, m.Numbered)
		if err != nil {
			return err
		}
		if targets, err = m.withNested(ctx, targets); err != nil {
			return err
		}
		if target := firstTarget(targets, func(t Target) bool { return Role(t.BeadID) == role }); target != nil {
			return m.EnsureTarget(ctx, *target)
		}
	}
	return fmt.Errorf("%w: no registered or cached workstream for role %q", ErrInvalidRoute, role)
}

// OpenByRole opens (without focusing) the Manager for a mail role and returns
// its chat pane, for the wake loop.
func (m *Manager) OpenByRole(ctx context.Context, role string) (string, error) {
	row, err := m.Store.GetWorkstreamManagerByRole(ctx, role)
	if err != nil {
		return "", err
	}
	if row == nil {
		return "", fmt.Errorf("no workstream Manager for role %q", role)
	}
	p, err := m.Store.GetProject(ctx, row.ProjectID)
	if err != nil || p == nil {
		return "", fmt.Errorf("project %d for %s: %v", row.ProjectID, role, err)
	}
	// The caller types into a tmux pane, so this never goes native.
	terminal := *m
	terminal.Native = nil
	res, err := terminal.Open(ctx, p.Name, row.WorkstreamBeadID, OpenOptions{})
	if err != nil {
		return "", err
	}
	return res.Pane, nil
}

// SyncNames renames Manager windows to the workstreams' current ranks.
func (m *Manager) SyncNames(ctx context.Context) error {
	targets, err := m.Workstreams(ctx)
	if err != nil {
		return err
	}
	names := make(map[string]layout.WorkstreamWindow, len(targets))
	for _, t := range targets {
		names[t.BeadID] = layout.WorkstreamWindow{Rank: t.Rank, Title: t.Title}
	}
	return m.Dir.SyncWorkstreamNames(ctx, names)
}

// AdoptResult reports an adoption.
type AdoptResult struct {
	Project     string `json:"project"`
	BeadID      string `json:"workstream_bead_id"`
	Role        string `json:"manager_role"`
	OldRole     string `json:"old_role"`
	SessionFile string `json:"session_file"`
	Window      string `json:"window"`
	Title       string `json:"title"`
	Rank        int    `json:"rank"`
}

// Adopt makes an already-running per-project Manager the Manager of one
// workstream without touching its process: the row points at its existing
// session file, its mail mapping is renamed to the workstream role (same am
// name, so no mail is lost), and its live window is tagged and renamed.
func (m *Manager) Adopt(ctx context.Context, projectName, workstreamBeadID, windowName string) (*AdoptResult, error) {
	target, err := m.find(ctx, func(t Target) bool { return t.BeadID == workstreamBeadID && t.Project.Name == projectName })
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, fmt.Errorf("%s is not an open workstream in %s", workstreamBeadID, projectName)
	}
	if target.Parent != "" {
		return nil, fmt.Errorf("%s is a nested workstream: only a top-level workstream adopts a per-project Manager", workstreamBeadID)
	}
	if err := m.validateTarget(ctx, *target); err != nil {
		return nil, err
	}
	oldSession := filepath.Join(target.Project.Path, ".commander", agentrt.SessionFileName("manager", agentrt.Claude))
	if _, err := os.Stat(oldSession); err != nil {
		return nil, fmt.Errorf("no existing Manager session to adopt at %s: %w", oldSession, err)
	}
	role := Role(workstreamBeadID)
	oldRole := "manager-" + flat(projectName)

	if err := m.Store.UpsertWorkstreamManager(ctx, store.WorkstreamManager{
		ProjectID: target.Project.ID, WorkstreamBeadID: workstreamBeadID, AmRole: role, SessionFile: oldSession,
	}); err != nil {
		return nil, err
	}
	// Keep the Manager's am name; only the role changes. If the new role is
	// already registered (opened before adoption), leave both as they are.
	if existing, _ := m.Store.GetAmAgent(ctx, m.AmKey, role); existing == "" {
		if err := m.Store.RenameAmRole(ctx, m.AmKey, oldRole, role); err != nil {
			return nil, err
		}
	}
	if _, err := m.Prepare(ctx, *target); err != nil {
		return nil, err
	}
	if windowName != "" {
		if err := m.Dir.TagWorkstreamWindow(ctx, windowName, workstreamBeadID, target.Rank, target.Title); err != nil {
			return nil, err
		}
	}
	return &AdoptResult{
		Project: projectName, BeadID: workstreamBeadID, Role: role, OldRole: oldRole,
		SessionFile: oldSession, Window: layout.WorkstreamWindowName(target.Rank, target.Title),
		Title: target.Title, Rank: target.Rank,
	}, nil
}

// flat mirrors activation.ManagerAgentName's name flattening.
func flat(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// ErrNoWorkstreams means a project has nothing to open a Manager for.
var ErrNoWorkstreams = errors.New("project has no open workstreams")

// OpenProject focuses the Manager of the project's highest-ranked open
// workstream (there is no per-project Manager any more).
func (m *Manager) OpenProject(ctx context.Context, projectName string) (*OpenResult, error) {
	targets, err := m.Workstreams(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range targets {
		if t.Project.Name == projectName {
			return m.Open(ctx, projectName, t.BeadID, OpenOptions{Focus: true})
		}
	}
	return nil, fmt.Errorf("%w: %q (create one to get a Manager)", ErrNoWorkstreams, projectName)
}

// NestedOwners lists a project's nested workstreams that would have Managers of
// their own (open kind:workstream beads under an open workstream, every one
// between them owning work too), parents first, from fresh reads. It does not
// consult the project's switch: the cutover asks before flipping it.
func NestedOwners(ctx context.Context, bc beads.Client, p store.Project) ([]Target, error) {
	list, err := bc.List(ctx, p.Path, beads.ListOptions{Labels: []string{beads.WorkstreamLabel}, Status: "all"})
	if err != nil {
		return nil, fmt.Errorf("nested workstreams of %s: %w", p.Name, err)
	}
	var ids []string
	byID := map[string]beads.Bead{}
	for _, b := range list {
		ids = append(ids, b.ID)
		byID[b.ID] = b
	}
	if len(ids) == 0 {
		return nil, nil
	}
	ds, err := beads.FreshDetails(ctx, bc, p.Path, ids)
	if err != nil {
		return nil, fmt.Errorf("nested workstreams of %s: %w", p.Name, err)
	}
	parents := beads.Parents{}
	for _, id := range ids {
		parents[id] = ds[id].Parent
	}
	known, err := fillParents(ctx, bc, p.Path, list, parents)
	if err != nil {
		return nil, fmt.Errorf("nested workstreams of %s: %w", p.Name, err)
	}
	ts := nestedOwned(list, known, parents, func(b beads.Bead, top, parent string) (Target, bool) {
		// The top: an open workstream, or a root not labelled one.
		up, known := byID[top]
		open := !known || (up.Status != "closed" && up.Status != beads.StatusDeferred && !beads.Superseded(up))
		return Target{Project: p, BeadID: b.ID, Title: b.Title, Parent: parent}, open
	})
	return ts, nil
}
