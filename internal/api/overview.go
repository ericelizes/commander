package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/example-user/commander/internal/linearapi"
	"github.com/example-user/commander/internal/linearsync"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/claudeacct"
	"github.com/example-user/commander/internal/config"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/overview"
	"github.com/example-user/commander/internal/store"
)

type overviewRequest struct {
	Project string `json:"project"`
	BeadID  string `json:"bead_id"`
	TrackID string `json:"track_id"`
	// Part is the tab asking: "status" (default) or "pitch".
	Part string `json:"part"`
	// Stream asks for newline-delimited results: one per change while the recap is
	// being written, ending when it is ready (or is waiting on something).
	Stream bool `json:"stream"`
}

// overviewWaiting is the answer while something the recap needs is still on its way.
// It is a plain wait the client retries, never a failure.
func overviewWaiting(for_ string) overview.Result {
	return overview.Result{State: "waiting", Evidence: []overview.Evidence{}, WaitingFor: for_}
}

// overviewSnapshot is the latest snapshot without waiting for a first one: cmdrd's
// first load after a restart can take minutes, longer than any reader of this
// route should hang. The load starts in the background, once.
func (s *Server) overviewSnapshot() (livedata.Snapshot, bool) {
	s.mu.Lock()
	have := s.version > 0
	cur := s.cur
	s.mu.Unlock()
	if !have && s.overviewKick.CompareAndSwap(false, true) {
		go func() {
			defer s.overviewKick.Store(false)
			s.Refresh(context.Background())
		}()
	}
	return cur, have
}

func (s *Server) serveOverview(w http.ResponseWriter, r *http.Request) {
	var q overviewRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&q); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid overview target"})
		return
	}
	snap, have := s.overviewSnapshot()
	if !have {
		s.writeOverview(w, q, overviewWaiting("the board to finish loading (cmdrd is still reading its first snapshot)"))
		return
	}
	if snap.Degraded != nil {
		s.writeOverview(w, q, overviewWaiting("live state to recover (the board could not be read just now)"))
		return
	}
	var ws *livedata.SnapWorkstream
	for i := range snap.Workstreams {
		x := &snap.Workstreams[i]
		if x.Project == q.Project && x.BeadID == q.BeadID {
			ws = x
			break
		}
	}
	if ws == nil {
		writeJSON(w, 404, map[string]string{"error": "workstream not found"})
		return
	}
	home := filepath.Dir(s.opts.ConfigPath)
	if s.opts.Chat != nil {
		home = s.opts.Chat.Home()
	}
	in, ok := buildOverviewInput(snap, ws, q, home)
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "nested workstream not found in this workstream"})
		return
	}
	in.Hold = snap.Agents[ws.ManagerRole].State == store.AgentWorking
	if !q.Stream {
		writeJSON(w, http.StatusOK, s.overviews.Get(in))
		return
	}
	s.streamOverview(w, r, in)
}

func (s *Server) writeOverview(w http.ResponseWriter, q overviewRequest, res overview.Result) {
	if q.Stream {
		w.Header().Set("Content-Type", "application/x-ndjson")
	}
	writeJSON(w, http.StatusOK, res)
}

// streamOverview writes the recap as it is written: a line per change, closed
// once nothing is being written. The client reopens it to keep the recap fresh.
func (s *Server) streamOverview(w http.ResponseWriter, r *http.Request, in overview.Input) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fl, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	for {
		changed := s.overviews.Changed() // taken first: a change between Get and the wait is not lost
		res := s.overviews.Get(in)
		if enc.Encode(res) != nil {
			return
		}
		if fl != nil {
			fl.Flush()
		}
		if res.State != "updating" {
			return
		}
		select {
		case <-changed:
		case <-r.Context().Done():
			return
		case <-deadline.C:
			return
		}
	}
}

func buildOverviewInput(snap livedata.Snapshot, ws *livedata.SnapWorkstream, q overviewRequest, home string) (overview.Input, bool) {
	in, ok := overviewInput(*ws, q.TrackID)
	if !ok {
		return in, false
	}
	if snap.Display != nil {
		for _, row := range snap.Display.Workstreams {
			if row.Project != q.Project || row.BeadID != q.BeadID {
				continue
			}
			var status any = map[string]any{"kind": row.Kind, "text": row.Text, "why": row.Why, "working": row.Working}
			if q.TrackID != "" {
				status = nil
				if dg := row.Diagram.Sub(q.TrackID); dg != nil {
					for _, st := range dg.Steps {
						if st.ID == dg.Current {
							status = map[string]any{"kind": st.Kind, "text": st.Text, "actor": st.Actor}
						}
					}
				}
				if row.Scope == q.TrackID {
					status = map[string]any{"kind": row.Kind, "text": row.Text, "why": row.Why, "working": row.Working}
				}
				for _, t := range row.AllTracks() {
					if t.BeadID == q.TrackID {
						status = map[string]any{"kind": t.Kind, "text": t.Text, "why": t.Why, "working": t.Working}
					}
				}
			}
			if status == nil {
				continue
			}
			raw, _ := json.Marshal(status)
			in.Evidence = append(in.Evidence, overview.Evidence{ID: "live-status", Label: "Current verified board status", Text: string(raw)})
		}
	}
	in.Evidence = append(in.Evidence, overviewMessages(home, ws.ManagerRole, q.TrackID)...)
	in.Evidence = append(in.Evidence, overviewPRSizes(*ws, q.TrackID)...)
	in.Evidence = append(in.Evidence, overviewLinearTickets(home, *ws, q.TrackID)...)
	in.Part = "status"
	if q.Part == "pitch" {
		// The pitch is scoping: it reads what the work is, not what moved a
		// minute ago, and is rewritten at most hourly.
		in.Part, in.Cooldown = "pitch", time.Hour
		kept := in.Evidence[:0]
		for _, e := range in.Evidence {
			if e.ID != "live-status" && !strings.HasPrefix(e.ID, "chat:") {
				kept = append(kept, e)
			}
		}
		in.Evidence = kept
	}
	in.Scope += ":" + in.Part
	now := time.Now()
	for i := range in.Evidence {
		in.Evidence[i].Text = livedata.HumanTimes(in.Evidence[i].Text, now)
	}
	return in, true
}

// prSizeLookup reads a pull request's size line from GitHub ("" when it cannot);
// tests replace it.
var prSizeLookup = ghPRSize

// prSizeTTL is how long a pull request's size is reused before GitHub is asked again.
const prSizeTTL = 10 * time.Minute

var prSizes = struct {
	sync.Mutex
	m map[string]prSizeEntry
}{m: map[string]prSizeEntry{}}

type prSizeEntry struct {
	at   time.Time
	text string
}

var repoName = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)

// overviewPRSizes is the size of every pull request in the scope (the
// workstream, or one nested workstream), so the recap's Scope can say how big
// the change is.
func overviewPRSizes(ws livedata.SnapWorkstream, track string) []overview.Evidence {
	steps := ws.Steps
	if track != "" {
		steps = nil
		var find func([]livedata.SnapStep)
		find = func(list []livedata.SnapStep) {
			for _, st := range list {
				if st.BeadID == track {
					steps = []livedata.SnapStep{st}
					return
				}
				find(st.Children)
			}
		}
		find(ws.Steps)
	}
	seen := map[string]bool{}
	var out []overview.Evidence
	var walk func([]livedata.SnapStep)
	walk = func(list []livedata.SnapStep) {
		for _, st := range list {
			repo := st.PRRepo
			if repo == "" {
				repo = ws.Repo
			}
			for _, ref := range st.PR {
				n, err := strconv.Atoi(strings.TrimPrefix(ref, "#"))
				key := fmt.Sprintf("%s#%d", repo, n)
				if err != nil || seen[key] || !repoName.MatchString(repo) {
					continue
				}
				seen[key] = true
				if text := cachedPRSize(repo, n); text != "" {
					out = append(out, overview.Evidence{ID: fmt.Sprintf("pr-size-%d", n), Label: fmt.Sprintf("Pull request #%d size (GitHub)", n), Text: text})
				}
			}
			walk(st.Children)
		}
	}
	walk(steps)
	return out
}

func cachedPRSize(repo string, n int) string {
	key := fmt.Sprintf("%s#%d", repo, n)
	prSizes.Lock()
	if e, ok := prSizes.m[key]; ok && time.Since(e.at) < prSizeTTL {
		prSizes.Unlock()
		return e.text
	}
	prSizes.Unlock()
	text := prSizeLookup(repo, n)
	prSizes.Lock()
	prSizes.m[key] = prSizeEntry{at: time.Now(), text: text}
	prSizes.Unlock()
	return text
}

func ghPRSize(repo string, n int) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gh", "pr", "view", strconv.Itoa(n), "-R", repo, "--json", "title,state,additions,deletions,changedFiles").Output()
	if err != nil {
		return ""
	}
	var pr struct {
		Title        string `json:"title"`
		State        string `json:"state"`
		Additions    int    `json:"additions"`
		Deletions    int    `json:"deletions"`
		ChangedFiles int    `json:"changedFiles"`
	}
	if json.Unmarshal(out, &pr) != nil {
		return ""
	}
	return fmt.Sprintf("Pull request #%d in %s (%s, %s): +%d / -%d lines across %d files", n, repo, pr.Title, strings.ToLower(pr.State), pr.Additions, pr.Deletions, pr.ChangedFiles)
}

func overviewInput(ws livedata.SnapWorkstream, track string) (overview.Input, bool) {
	in := overview.Input{Scope: ws.Project + ":" + ws.BeadID + ":" + track, Goal: ws.Title}
	steps := ws.Steps
	if track != "" {
		var found *livedata.SnapStep
		var walk func([]livedata.SnapStep)
		walk = func(list []livedata.SnapStep) {
			for i := range list {
				if list[i].BeadID == track && list[i].Track {
					found = &list[i]
					return
				}
				walk(list[i].Children)
			}
		}
		walk(steps)
		if found == nil {
			return in, false
		}
		in.Goal = found.Title
		steps = []livedata.SnapStep{*found}
	}
	add := func(id, label string, v any) {
		b, _ := json.Marshal(v)
		in.Evidence = append(in.Evidence, overview.Evidence{ID: id, Label: label, Text: string(b)})
	}
	if track == "" {
		add("scope", "Recorded workstream outcome", map[string]string{"title": ws.Title, "status": ws.Status, "note": trimOverview(ws.Note, 4000)})
	}

	// Prefer current/open work so a long history cannot crowd the live blocker out.
	var candidates []livedata.SnapStep
	parentOf := map[string]string{}
	var walk func([]livedata.SnapStep, string)
	walk = func(list []livedata.SnapStep, parent string) {
		for _, st := range list {
			if st.Task || st.Superseded {
				continue
			}
			parentOf[st.BeadID] = parent
			candidates = append(candidates, st)
			walk(st.Children, st.Title)
		}
	}
	walk(steps, "")
	// A bare bead id means nothing to the reader, so a wait or a blocker is named by its title.
	known := map[string]overviewBead{}
	var names func([]livedata.SnapStep)
	names = func(list []livedata.SnapStep) {
		for _, st := range list {
			known[st.BeadID] = overviewBead{title: st.Title, done: st.Status == "closed" || st.Status == beads.StatusDeferred || st.Superseded}
			for _, id := range st.DoneBlockers {
				if _, ok := known[id]; !ok {
					known[id] = overviewBead{done: true} // finished and folded off the board
				}
			}
			names(st.Children)
		}
	}
	names(ws.Steps)
	for _, g := range ws.GateNodes {
		if _, ok := known[g.BeadID]; !ok {
			known[g.BeadID] = overviewBead{title: "approval gate: " + g.Name, done: g.Status == "passed"}
		}
	}
	priority := func(st livedata.SnapStep) int {
		if st.BeadID == ws.NowStep {
			return 0
		}
		if st.Status != "closed" && (st.Working || st.Broken || st.Waiting != "" || st.State == "needs-user") {
			return 1
		}
		if st.Status != "closed" {
			return 2
		}
		return 3
	}
	sort.SliceStable(candidates, func(i, j int) bool { return priority(candidates[i]) < priority(candidates[j]) })
	finished := overviewFinishedTasks(steps, priority)
	for _, st := range candidates {
		if len(in.Evidence) >= overviewMaxEvidence-len(finished) {
			break
		}
		add(st.BeadID, st.Title, overviewStepRecord(st, parentOf[st.BeadID], known))
	}
	for _, f := range finished {
		add(f.task.BeadID, f.task.Title, map[string]any{"kind": "finished Operator task", "title": f.task.Title, "part_of": f.parent, "outcome": f.task.CloseReason})
	}
	return in, true
}

const (
	overviewMaxEvidence = 48
	// overviewMaxFinished bounds the finished Operator tasks the reader sees: their
	// close reasons are often the only record of what a step's work found.
	overviewMaxFinished = 12
)

type overviewBead struct {
	title string
	done  bool // closed, parked or superseded: no longer something to wait for
}

type overviewFinished struct {
	task   livedata.SnapStep
	parent string
	rank   int
	order  int
}

// overviewFinishedTasks picks the latest finished Operator tasks that say what they
// ended with: those under the live steps first, then the most recent in plan order.
func overviewFinishedTasks(steps []livedata.SnapStep, priority func(livedata.SnapStep) int) []overviewFinished {
	var out []overviewFinished
	var walk func([]livedata.SnapStep, *livedata.SnapStep)
	walk = func(list []livedata.SnapStep, parent *livedata.SnapStep) {
		for _, st := range list {
			if st.Superseded {
				continue
			}
			if st.Task && st.Status == "closed" && strings.TrimSpace(st.CloseReason) != "" {
				f := overviewFinished{task: st, rank: 3, order: len(out)}
				if parent != nil {
					f.parent, f.rank = parent.Title, priority(*parent)
				}
				out = append(out, f)
			}
			// Tasks under a closed step are folded off the board; the step carries the latest.
			for i := len(st.FinishedTasks) - 1; i >= 0; i-- {
				t := st.FinishedTasks[i]
				out = append(out, overviewFinished{task: livedata.SnapStep{BeadID: t.BeadID, Title: t.Title, CloseReason: t.Outcome}, parent: st.Title, rank: priority(st), order: len(out)})
			}
			walk(st.Children, &st)
		}
	}
	walk(steps, nil)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].rank != out[j].rank {
			return out[i].rank < out[j].rank
		}
		return out[i].order > out[j].order
	})
	if len(out) > overviewMaxFinished {
		out = out[:overviewMaxFinished]
	}
	return out
}

// overviewMessages reads a bounded tail, never tool payloads or credential files.
func overviewMessages(home, role, track string) []overview.Evidence {
	if !chat.ValidRole(role) {
		return nil
	}
	f, err := os.Open(filepath.Join(home, "chat", role+".jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil
	}
	start := stat.Size() - 256<<10
	if start < 0 {
		start = 0
	}
	if _, err = f.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(f, 256<<10))
	lines := strings.Split(string(b), "\n")
	if start > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	var out []overview.Evidence
	for _, line := range lines {
		var e chat.Event
		if json.Unmarshal([]byte(line), &e) != nil || e.Kind != chat.KindText || e.ParentToolUseID != "" || strings.TrimSpace(e.Text) == "" {
			continue
		}
		// Track recaps cannot borrow an unrelated track's Manager narrative.
		if track != "" && !strings.Contains(e.Text, track) {
			continue
		}
		out = append(out, overview.Evidence{ID: fmt.Sprintf("chat:%d", e.Seq), Label: "Manager report (not independently verified)", Text: trimOverview(e.Text, 4000), At: e.Time.UTC().Format("2006-01-02T15:04:05Z")})
	}
	if len(out) > 5 {
		out = out[len(out)-5:]
	}
	return out
}

// overviewStepRecord is what the reader may say about a step. The pull requests, tickets,
// criteria and the titles of what it waits on are what let the recap say what a named
// thing is, not only that it exists.
func overviewStepRecord(st livedata.SnapStep, parent string, known map[string]overviewBead) map[string]any {
	var after []string
	others := 0
	for _, id := range st.BlockedBy {
		b, ok := known[id]
		switch {
		case !ok:
			others++ // outside this workstream: its state is not in the record
		case !b.done:
			after = append(after, b.title)
		}
	}
	rec := map[string]any{"title": st.Title, "state": st.State, "status": st.Status, "note": trimOverview(st.Note, 2000), "waiting": st.Waiting, "working": st.Working, "owner_state": st.OwnerState, "owner_why": st.OwnerWhy, "review": st.ReviewFact, "waits_for": after, "gates": st.Gates}
	if others > 0 {
		rec["also_waits_on_beads_outside_this_workstream"] = others
	}
	if parent != "" {
		rec["part_of"] = parent
	}
	if st.CloseReason != "" {
		rec["outcome"] = st.CloseReason
	}
	if len(st.PR) > 0 {
		rec["pull_requests"] = st.PR
		rec["pull_request_repo"] = st.PRRepo
	}
	if len(st.Tickets) > 0 {
		rec["tickets"] = st.Tickets
	}
	if st.Criteria != "" {
		rec["done_when"] = trimOverview(st.Criteria, 1000)
	}
	return rec
}

func trimOverview(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func overviewGenerator(opts Options) overview.Generate {
	if opts.OverviewGenerate != nil {
		return opts.OverviewGenerate
	}
	home := filepath.Dir(opts.ConfigPath)
	if opts.Chat != nil {
		home = opts.Chat.Home()
	}
	prompt := overview.Prompt
	if cfg, err := config.Load(opts.ConfigPath); err == nil {
		if name := strings.TrimSpace(config.UserName(cfg)); name != "" {
			prompt += "\nThe reader is " + name + ": write 'you' and 'your' wherever the records mean them, by first or full name; never their name."
		}
	}
	return func(ctx context.Context, in overview.Input) (overview.Recap, error) {
		return overview.ClaudeWith(prompt, claudeacct.Env(home, chat.ReadAccount(home, "commander")))(ctx, in)
	}
}

// linearTicketLookup reads a Linear ticket's state, title and description with
// the saved key ("" when it cannot); tests replace it.
var linearTicketLookup = fetchLinearTicket

var linearTickets = struct {
	sync.Mutex
	m map[string]prSizeEntry
}{m: map[string]prSizeEntry{}}

// overviewLinearTickets is each Linear ticket the scope names, as the user
// wrote it: the problem, the measurements and the acceptance criteria.
func overviewLinearTickets(home string, ws livedata.SnapWorkstream, track string) []overview.Evidence {
	var ids []string
	seen := map[string]bool{}
	addIDs := func(title string, labels []string) {
		for _, id := range append(ticketID.FindAllString(title, -1), labels...) {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	var walk func([]livedata.SnapStep)
	walk = func(steps []livedata.SnapStep) {
		for _, st := range steps {
			addIDs(st.Title, st.Tickets)
			walk(st.Children)
		}
	}
	if track == "" {
		addIDs(ws.Title, ws.Tickets)
		walk(ws.Steps)
	} else {
		var find func([]livedata.SnapStep) bool
		find = func(steps []livedata.SnapStep) bool {
			for _, st := range steps {
				if st.BeadID == track {
					addIDs(st.Title, st.Tickets)
					walk(st.Children)
					return true
				}
				if find(st.Children) {
					return true
				}
			}
			return false
		}
		find(ws.Steps)
	}
	if len(ids) > 4 {
		ids = ids[:4]
	}
	var out []overview.Evidence
	for _, id := range ids {
		linearTickets.Lock()
		e, ok := linearTickets.m[id]
		linearTickets.Unlock()
		if !ok || time.Since(e.at) > 10*time.Minute {
			e = prSizeEntry{at: time.Now(), text: linearTicketLookup(home, id)}
			linearTickets.Lock()
			linearTickets.m[id] = e
			linearTickets.Unlock()
		}
		if e.text != "" {
			out = append(out, overview.Evidence{ID: "linear-" + id, Label: "Linear ticket " + id + ": the user's own problem statement, measurements and acceptance criteria", Text: e.text})
		}
	}
	return out
}

func fetchLinearTicket(home, id string) string {
	key, err := linearsync.ReadKey(home)
	if err != nil || key == "" {
		return ""
	}
	payload, _ := json.Marshal(map[string]any{
		"query":     "query($id: String!) { issue(id: $id) { identifier title description state { name } } }",
		"variables": map[string]string{"id": id},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	body, status := linearapi.Query(ctx, nil, linearapi.Endpoint, key, payload)
	if status/100 != 2 {
		return ""
	}
	body = []byte(strings.ReplaceAll(string(body), key, "[redacted]"))
	var data struct {
		Data struct {
			Issue *struct {
				Identifier  string `json:"identifier"`
				Title       string `json:"title"`
				Description string `json:"description"`
				State       struct {
					Name string `json:"name"`
				} `json:"state"`
			} `json:"issue"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &data) != nil || data.Data.Issue == nil {
		return ""
	}
	is := data.Data.Issue
	return fmt.Sprintf("%s (%s): %s\n%s", is.Identifier, is.State.Name, is.Title, trimOverview(is.Description, 6000))
}
