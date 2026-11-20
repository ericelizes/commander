package brief

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
)

// Other is the workstream key of evidence that belongs to no workstream.
const Other = "other"

// Evidence is one record of the writer's briefing. ID is how the Brief cites
// it (bead:<id> or linear:<ID>); Ref names the record itself (step:<id>,
// report:<project>:<id>, week:<project>:<id>...). When says which section it
// can back: today, yesterday, week, board (the live board now) or later (a
// wait that does not come due today).
type Evidence struct {
	ID         string `json:"id"`
	Ref        string `json:"ref,omitempty"`
	Workstream string `json:"workstream"` // its workstream's name, or Other
	When       string `json:"when"`
	Label      string `json:"label"`
	Text       string `json:"text"`
	At         string `json:"at,omitempty"`
}

// Workstream is a group the Brief is written under.
type Workstream struct {
	Key  string `json:"-"`
	Name string `json:"name"`
	Goal string `json:"goal,omitempty"`
	// Progress is the board's share done (0-100); nil when it is not on the board.
	Progress *int `json:"percent_done,omitempty"`
	Done     bool `json:"done,omitempty"`
}

// Input is what the reader gets.
type Input struct {
	Now         string       `json:"now"`
	Today       string       `json:"today"`
	Yesterday   string       `json:"yesterday"`
	Week        string       `json:"week"`
	Workstreams []Workstream `json:"workstreams"`
	Evidence    []Evidence   `json:"evidence"`
	// ids are the bead ids the evidence names; prose must not show them.
	ids []string
	// whens is each known bead's windows: board, later, today, yesterday, week.
	whens map[string]map[string]bool
}

// Ticket is a Linear ticket assigned to the user that changed in the window.
type Ticket struct {
	ID          string
	Title       string
	State       string
	StateType   string
	UpdatedAt   time.Time
	StartedAt   time.Time
	CompletedAt time.Time
}

// Sources are the raw records a Brief is written from.
type Sources struct {
	Beads   []store.BriefBead
	Reports []store.BriefReport
	Tree    livedata.Tree
	// WaitUntil is each step's earliest time wait (unix s), from the snapshot.
	WaitUntil map[string]int64
	// PRs are each step's pull requests ("owner/repo#12"), from the snapshot.
	PRs    map[string][]string
	Linear []Ticket
	// LinearNote says why Linear is not in the Brief ("" when it is).
	LinearNote string
}

const (
	maxClosedPerGroup  = 40 // finished beads per workstream per window (process steps left out)
	maxReportsPerGroup = 8  // final reports per workstream per window
	maxStepsPerGroup   = 10 // open Now/Next steps per workstream
	maxWeekTitles      = 10
)

// ticketParens is a parenthesis that names a ticket: "(ENGCHA-5337)", "(Hunter: ENGINT-3263)".
var ticketParens = regexp.MustCompile(`\s*\([^()]*\b[A-Z][A-Z0-9]*-\d+\b[^()]*\)`)

// ShortName is a workstream's title as a heading: no ticket ids, and only the
// name before a colon when there is one.
func ShortName(title string) string {
	t := strings.TrimSpace(ticketParens.ReplaceAllString(title, ""))
	if i := strings.Index(t, ": "); i >= 4 && i <= 48 && strings.Count(t[:i], "(") == strings.Count(t[:i], ")") {
		t = t[:i]
	}
	return t
}

func trim(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func hasLabel(labels []string, l string) bool {
	for _, x := range labels {
		if x == l {
			return true
		}
	}
	return false
}

func clock(t time.Time) string { return t.In(Zone).Format("Mon Jan 2 3:04 PM") }

func record(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Assemble turns the sources into the reader's input: the workstreams in board
// order, and each record tagged with its workstream and the window it backs.
func Assemble(w Windows, src Sources) Input {
	in := Input{
		Now:       clock(w.Now),
		Today:     w.Today.Label,
		Yesterday: w.Yesterday.Label,
		Week:      w.Week.Label,
	}
	reg := map[string]*Workstream{}
	var order []string
	addWS := func(key, title string) *Workstream {
		if ws := reg[key]; ws != nil {
			return ws
		}
		ws := &Workstream{Key: key, Name: ShortName(title)}
		reg[key] = ws
		order = append(order, key)
		return ws
	}
	ids := map[string]bool{}

	// The board: each top-level workstream's goal and progress, and its open Now/Next steps.
	top := map[string]string{}    // row id -> its top-level workstream key
	topRow := map[string]string{} // row id -> its top-level row id
	title := map[string]string{}  // row id -> title
	ticketWS := map[string]string{}
	children := map[string][]map[string]any{}
	for _, r := range src.Tree.Rows {
		ids[r.ID] = true
		title[r.ID] = r.Title
		if r.Depth == 0 || top[r.Parent] == "" {
			top[r.ID], topRow[r.ID] = r.Key, r.ID
		} else {
			top[r.ID], topRow[r.ID] = top[r.Parent], topRow[r.Parent]
		}
		for _, t := range r.Tickets {
			if ticketWS[t] == "" {
				ticketWS[t] = top[r.ID]
			}
		}
		if r.Depth == 1 {
			children[top[r.ID]] = append(children[top[r.ID]], map[string]any{"title": r.Title, "percent_done": r.Progress, "done": r.Done})
		}
	}
	for _, r := range src.Tree.Rows {
		if top[r.ID] != r.Key {
			continue
		}
		ws := addWS(r.Key, r.Title)
		ws.Goal = trim(strings.Join(r.Criteria, " "), 600)
		pct := r.Progress
		ws.Progress, ws.Done = &pct, r.Done
		var status []string
		for _, l := range r.Status {
			status = append(status, l.Text)
		}
		rec := map[string]any{"workstream": r.Title, "acceptance_criteria": ws.Goal, "percent_done": r.Progress, "of": r.Of, "done": r.Done, "state": r.State, "holding_it_now": status}
		if r.Line.Tone == "sum" {
			rec["summary"] = r.Line.Text
		}
		if kids := children[r.Key]; len(kids) > 0 {
			rec["nested_workstreams"] = kids
		}
		in.Evidence = append(in.Evidence, Evidence{ID: "goal:" + r.Key, Workstream: r.Key, When: "board", Label: "The board's record of this workstream: goal, progress and what holds it", Text: record(rec)})
	}
	steps := append([]livedata.TreeStep(nil), src.Tree.Steps...)
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].Col < steps[j].Col })
	perWS := map[string]int{}
	for _, s := range steps {
		ids[s.ID] = true
		key := top[s.Row]
		if s.State == "done" || s.Col < 0 || s.Col > 1 || key == "" || perWS[key] >= maxStepsPerGroup {
			continue
		}
		perWS[key]++
		rec := map[string]any{"step": s.Title, "column": livedata.ColumnName(s.Col), "owner": s.Own, "kind": s.Type, "state": s.State}
		if s.Row != topRow[s.Row] {
			rec["part_of"] = title[s.Row]
		}
		if s.Wait != "" {
			rec["waiting"] = s.Wait
		}
		if s.Ask != "" {
			rec["asks_you"] = s.Ask
		}
		if prs := src.PRs[s.ID]; len(prs) > 0 {
			rec["pull_requests"] = prs
		}
		if s.Since > 0 && s.State == "active" {
			rec["active_since"] = clock(time.Unix(s.Since, 0))
		}
		when := "board"
		if u := src.WaitUntil[s.ID]; u > 0 {
			at := time.Unix(u, 0)
			rec["waits_until"] = clock(at)
			today := sameDay(at, w.Now, Zone) || at.Before(w.Now)
			rec["happens_today"] = today
			if !today {
				when = "later"
			}
		}
		in.Evidence = append(in.Evidence, Evidence{ID: "step:" + s.ID, Workstream: key, When: when, Label: livedata.ColumnName(s.Col) + " on the board: " + s.Title, Text: record(rec)})
	}

	// Beads: what finished, by day, and each workstream's week.
	beadTitle := map[string]string{}
	beadWS := map[string]string{}
	type weekAgg struct {
		closed   int
		finished []string // workstreams finished this week
		steps    []string // newest finished steps first
	}
	week := map[string]*weekAgg{}
	type group struct{ ws, when string }
	var closed = map[group][]store.BriefBead{}
	for _, b := range src.Beads {
		ids[b.ID] = true
		key := Other
		if b.Workstream != "" {
			key = b.Project + ":" + b.Workstream
			addWS(key, b.WorkstreamTitle)
		}
		beadTitle[b.Project+":"+b.ID] = b.Title
		beadWS[b.Project+":"+b.ID] = key
		for _, l := range b.Labels {
			if t, ok := strings.CutPrefix(l, "ticket:"); ok && ticketWS[t] == "" {
				ticketWS[t] = key
			}
		}
		if b.Status != "closed" || hasLabel(b.Labels, "superseded") {
			continue
		}
		if hasLabel(b.Labels, "gate") && !hasLabel(b.Labels, "approver:user") {
			continue // an automatic gate: the step it guards says what happened
		}
		when := w.When(b.UpdatedAt)
		if when == "" {
			continue
		}
		if !b.UpdatedAt.Before(w.Week.Start) {
			a := week[key]
			if a == nil {
				a = &weekAgg{}
				week[key] = a
			}
			a.closed++
			if hasLabel(b.Labels, "kind:workstream") {
				a.finished = append(a.finished, b.Title)
				if ws := reg[key]; ws != nil && b.Project+":"+b.ID == key {
					ws.Done = true
				}
			} else if !hasLabel(b.Labels, "gate") {
				a.steps = append(a.steps, b.Title)
			}
		}
		if (when == "today" || when == "yesterday") && !processStep(b) {
			closed[group{key, when}] = append(closed[group{key, when}], b)
		}
	}
	keys := make([]group, 0, len(closed))
	for g := range closed {
		keys = append(keys, g)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].ws+keys[i].when < keys[j].ws+keys[j].when })
	for _, g := range keys {
		list := closed[g]
		// Finished workstreams first, then the newest.
		sort.SliceStable(list, func(i, j int) bool {
			wi, wj := hasLabel(list[i].Labels, "kind:workstream"), hasLabel(list[j].Labels, "kind:workstream")
			if wi != wj {
				return wi
			}
			return list[i].UpdatedAt.After(list[j].UpdatedAt)
		})
		if len(list) > maxClosedPerGroup {
			list = list[:maxClosedPerGroup]
		}
		for _, b := range list {
			kind, label := "step", "Step finished"
			switch {
			case hasLabel(b.Labels, "kind:workstream"):
				kind, label = "workstream", "Workstream finished"
			case hasLabel(b.Labels, "gate"):
				kind, label = "your approval", "Approval you gave"
			case hasLabel(b.Labels, "review"):
				kind, label = "review", "Review finished"
			case hasLabel(b.Labels, "kind:task"):
				kind, label = "agent task", "Agent task finished"
			}
			rec := map[string]any{"title": b.Title, "kind": kind, "finished_at": clock(b.UpdatedAt)}
			if b.Parent != "" {
				rec["part_of"] = b.Parent
			}
			if b.CloseReason != "" {
				rec["outcome"] = trim(b.CloseReason, 300)
			}
			in.Evidence = append(in.Evidence, Evidence{ID: "bead:" + b.Project + ":" + b.ID, Workstream: g.ws, When: g.when, Label: label + ": " + b.Title, Text: record(rec), At: b.UpdatedAt.UTC().Format(time.RFC3339)})
		}
	}

	// Final reports: the agent's own account of what a task did, latest per bead.
	latest := map[string]store.BriefReport{}
	for _, r := range src.Reports {
		latest[r.Project+":"+r.BeadID] = r
	}
	reportsBy := map[group][]store.BriefReport{}
	for _, r := range latest {
		when := w.When(r.CreatedAt)
		if when != "today" && when != "yesterday" {
			continue
		}
		key := beadWS[r.Project+":"+r.BeadID]
		if key == "" {
			key = Other
		}
		reportsBy[group{key, when}] = append(reportsBy[group{key, when}], r)
	}
	rkeys := make([]group, 0, len(reportsBy))
	for g := range reportsBy {
		rkeys = append(rkeys, g)
	}
	sort.Slice(rkeys, func(i, j int) bool { return rkeys[i].ws+rkeys[i].when < rkeys[j].ws+rkeys[j].when })
	for _, g := range rkeys {
		list := reportsBy[g]
		sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
		if len(list) > maxReportsPerGroup {
			list = list[:maxReportsPerGroup]
		}
		for _, r := range list {
			t := beadTitle[r.Project+":"+r.BeadID]
			rec := map[string]any{"task": t, "report": trim(strings.TrimPrefix(r.Text, "Final report:"), 350), "written_at": clock(r.CreatedAt)}
			in.Evidence = append(in.Evidence, Evidence{ID: "report:" + r.Project + ":" + r.BeadID, Workstream: g.ws, When: g.when, Label: "Agent's final report (its own account): " + t, Text: record(rec), At: r.CreatedAt.UTC().Format(time.RFC3339)})
		}
	}

	// Linear: the user's tickets that moved: finished, started, or in progress
	// now. An edit alone (triage, a comment) is not progress.
	for _, t := range src.Linear {
		key := ticketWS[t.ID]
		if key == "" {
			key = Other
		}
		when := ""
		switch {
		case t.StateType == "completed":
			when = w.When(t.CompletedAt)
		case t.StateType == "started":
			when = "board"
		}
		if when == "" {
			continue
		}
		at := t.UpdatedAt
		if !t.CompletedAt.IsZero() {
			at = t.CompletedAt
		}
		rec := map[string]any{"ticket": t.Title, "status": t.State, "last_changed": clock(t.UpdatedAt)}
		if !t.StartedAt.IsZero() {
			rec["started"] = clock(t.StartedAt)
		}
		if !t.CompletedAt.IsZero() {
			rec["completed"] = clock(t.CompletedAt)
		}
		in.Evidence = append(in.Evidence, Evidence{ID: "linear:" + t.ID, Workstream: key, When: when, Label: "Linear ticket assigned to you (" + t.State + "): " + t.Title, Text: record(rec), At: at.UTC().Format(time.RFC3339)})
	}

	// Each workstream's week, in one record.
	for _, key := range append(append([]string{}, order...), Other) {
		a := week[key]
		if a == nil {
			continue
		}
		if len(a.steps) > 1 {
			// newest first: the beads came oldest first
			for i, j := 0, len(a.steps)-1; i < j; i, j = i+1, j-1 {
				a.steps[i], a.steps[j] = a.steps[j], a.steps[i]
			}
		}
		if len(a.steps) > maxWeekTitles {
			a.steps = a.steps[:maxWeekTitles]
		}
		rec := map[string]any{"finished_this_week": a.closed, "workstreams_finished": a.finished, "latest_finished_steps": a.steps}
		in.Evidence = append(in.Evidence, Evidence{ID: "week:" + key, Workstream: key, When: "week", Label: fmt.Sprintf("What finished this week (%s)", w.Week.Label), Text: record(rec)})
	}

	used := map[string]bool{}
	for _, e := range in.Evidence {
		used[e.Workstream] = true
	}
	for _, key := range order {
		if used[key] {
			in.Workstreams = append(in.Workstreams, *reg[key])
		}
	}
	// The writer cites beads and tickets by id and reads workstreams by name.
	for i := range in.Evidence {
		e := &in.Evidence[i]
		e.Ref, e.ID = e.ID, citeOf(e.ID)
		name := "Other"
		if ws := reg[e.Workstream]; ws != nil {
			name = ws.Name
		}
		e.Workstream = name
	}
	for id := range ids {
		if len(id) >= 5 && strings.Contains(id, "-") { // a bead id, never an ordinary word
			in.ids = append(in.ids, id)
		}
	}
	sort.Strings(in.ids)

	// Which windows each bead can back, from every record read, not only those
	// the briefing kept.
	in.whens = map[string]map[string]bool{}
	mark := func(id, when string) {
		if when == "" {
			return
		}
		if in.whens[id] == nil {
			in.whens[id] = map[string]bool{}
		}
		in.whens[id][when] = true
	}
	for _, r := range src.Tree.Rows {
		mark(r.ID, "board")
	}
	for _, st := range src.Tree.Steps {
		if st.State == "done" {
			continue
		}
		when := "board"
		if u := src.WaitUntil[st.ID]; u > 0 && !sameDay(time.Unix(u, 0), w.Now, Zone) && time.Unix(u, 0).After(w.Now) {
			when = "later"
		}
		mark(st.ID, when)
	}
	for _, b := range src.Beads {
		mark(b.ID, w.When(b.UpdatedAt))
	}
	for _, r := range src.Reports {
		mark(r.BeadID, w.When(r.CreatedAt))
	}
	return in
}

// citeOf is how the Brief cites a record: its bead, or its Linear ticket.
func citeOf(ref string) string {
	kind, rest, _ := strings.Cut(ref, ":")
	switch kind {
	case "linear":
		return ref
	case "step":
		return "bead:" + rest
	}
	if _, id, ok := strings.Cut(rest, ":"); ok && id != Other {
		return "bead:" + id // goal:, bead:, report:, week: <project>:<id>
	}
	return ref
}

// processStep is a finished step that is only process (a review, a revision, a
// wait, keeping a PR green), not an outcome. Nov 20: Forge Notify closed 60+
// steps in a day, and the reviews and waits crowded its real work (phase 2 on,
// per-app alerting built, CI notifications deployed) out of the Brief.
func processStep(b store.BriefBead) bool {
	if hasLabel(b.Labels, "review") || hasLabel(b.Labels, "kind:workstream") {
		return hasLabel(b.Labels, "review")
	}
	return processTitle.MatchString(b.Title)
}

var processTitle = regexp.MustCompile(`(?i)^(review\b|revise\b|wait\b|daily recheck|re-?check\b|get .*\b(green|passing)\b|keep .*\bgreen\b|resolve .*\b(threads|conflicts)\b|bring .* up to date)`)
