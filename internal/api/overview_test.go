package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/overview"
)

func overviewFixture() livedata.SnapWorkstream {
	return livedata.SnapWorkstream{Project: "p", BeadID: "w", Title: "Whole outcome", ManagerRole: "manager-w", Steps: []livedata.SnapStep{
		{BeadID: "track-a", Track: true, Title: "Dashboards", Children: []livedata.SnapStep{{BeadID: "a-fix", Title: "Button published", Status: "closed"}}},
		{BeadID: "track-b", Track: true, Title: "Payroll", Children: []livedata.SnapStep{{BeadID: "b-fix", Title: "Private payroll detail"}}},
	}}
}
func TestOverviewUsesActualTrackAncestryNotDottedGuess(t *testing.T) {
	in, ok := overviewInput(overviewFixture(), "track-a")
	if !ok {
		t.Fatal("track missing")
	}
	raw, _ := json.Marshal(in)
	if strings.Contains(string(raw), "Payroll") || !strings.Contains(string(raw), "Button published") {
		t.Fatal(string(raw))
	}
	if _, ok = overviewInput(overviewFixture(), "w.guessed"); ok {
		t.Fatal("accepted guessed track")
	}
}
func TestOverviewEndpointRequiresAuthAndKnownScope(t *testing.T) {
	s := New(Options{Token: "token", Source: func(context.Context) livedata.Snapshot {
		return livedata.Snapshot{Workstreams: []livedata.SnapWorkstream{overviewFixture()}}
	}, OverviewGenerate: func(context.Context, overview.Input) (overview.Recap, error) { return overview.Recap{}, nil }})
	s.Refresh(context.Background())
	for _, tc := range []struct {
		token, body string
		want        int
	}{{"", `{"project":"p","bead_id":"w"}`, 401}, {"token", `{"project":"p","bead_id":"not-w"}`, 404}, {"token", `{"project":"p","bead_id":"w","track_id":"track-bad"}`, 404}, {"token", `{"project":"p","bead_id":"w","track_id":"track-a"}`, 200}} {
		r := httptest.NewRequest("POST", "/api/overview", strings.NewReader(tc.body))
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s: %d %s", tc.body, w.Code, w.Body.String())
		}
	}
}
func TestOverviewMessagesSkipToolsAndCrossTrackNarrative(t *testing.T) {
	h := t.TempDir()
	os.MkdirAll(filepath.Join(h, "chat"), 0700)
	lines := `{"kind":"tool_result","seq":1,"text":"secret tool data"}
{"kind":"text","seq":2,"text":"track-b payroll has changed"}
{"kind":"text","seq":3,"text":"track-a button published","time":"2026-11-04T01:00:00Z"}
`
	os.WriteFile(filepath.Join(h, "chat", "manager-w.jsonl"), []byte(lines), 0600)
	got := overviewMessages(h, "manager-w", "track-a")
	if len(got) != 1 || got[0].ID != "chat:3" {
		t.Fatal(got)
	}
	if got := overviewMessages(h, "../../outside", ""); len(got) != 0 {
		t.Fatal("accepted invalid role")
	}
}

func TestOverviewLongHistoryCannotHideCurrentBlocker(t *testing.T) {
	ws := overviewFixture()
	ws.Steps = nil
	for i := 0; i < 80; i++ {
		ws.Steps = append(ws.Steps, livedata.SnapStep{BeadID: fmt.Sprint("past-", i), Status: "closed", Title: "Historical setup"})
	}
	ws.Steps = append(ws.Steps, livedata.SnapStep{BeadID: "current", Title: "Actual current blocker", Status: "open", Waiting: "CI"})
	ws.NowStep = "current"
	in, _ := overviewInput(ws, "")
	raw, _ := json.Marshal(in)
	if !strings.Contains(string(raw), "Actual current blocker") || len(in.Evidence) > 48 {
		t.Fatal(string(raw))
	}
}

func TestOverviewStepRecordLetsTheReaderExplainWhatItNames(t *testing.T) {
	ws := livedata.SnapWorkstream{Project: "p", BeadID: "w", Title: "Backups", ManagerRole: "manager-w", Steps: []livedata.SnapStep{
		{BeadID: "t", Track: true, Title: "Backup fix", Children: []livedata.SnapStep{
			{BeadID: "alert", Title: "Backup failures raise an alert", Status: "open"},
			{BeadID: "old", Title: "Old disk check", Status: "closed"},
			{BeadID: "pr", Title: "Platform change shipped", Status: "open", BlockedBy: []string{"alert", "old", "approve", "passed-gate", "gone"}, PR: []string{"123"}, PRRepo: "acme/platform", Tickets: []string{"TEAM-12"}, Criteria: "A failed backup stays visible after a restart"},
		}},
	}, GateNodes: []livedata.SnapGateNode{{BeadID: "approve", Name: "Rollout approved", Status: "open"}, {BeadID: "passed-gate", Name: "Spec approved", Status: "passed"}}}
	in, ok := overviewInput(ws, "t")
	if !ok {
		t.Fatal("track missing")
	}
	var rec map[string]any
	for _, e := range in.Evidence {
		if e.ID == "pr" {
			json.Unmarshal([]byte(e.Text), &rec)
		}
	}
	raw, _ := json.Marshal(rec)
	for _, want := range []string{`"pull_requests":["123"]`, `"pull_request_repo":"acme/platform"`, `"tickets":["TEAM-12"]`, `"part_of":"Backup fix"`, `"waits_for":["Backup failures raise an alert","approval gate: Rollout approved"]`, `"also_waits_on_beads_outside_this_workstream":1`, "stays visible after a restart"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing %s in %s", want, raw)
		}
	}
	if strings.Contains(string(raw), `"alert"`) || strings.Contains(string(raw), "gone") {
		t.Fatalf("bare bead ids leaked to the reader: %s", raw)
	}
	if strings.Contains(string(raw), "Old disk check") || strings.Contains(string(raw), "Spec approved") {
		t.Fatalf("a closed blocker or passed gate reads as a current wait: %s", raw)
	}
}

// What a step's work found often lives only in its Operator task's close reason
// (a regeneration that ran and changed nothing): the reader must see it, bounded.
func TestOverviewReaderSeesFinishedTaskOutcomes(t *testing.T) {
	step := livedata.SnapStep{BeadID: "s", Title: "Regenerate the report templates", Status: "in_progress", Working: true}
	for i := 0; i < 20; i++ {
		step.Children = append(step.Children, livedata.SnapStep{BeadID: fmt.Sprint("s.", i), Task: true, Status: "closed", Title: fmt.Sprint("Run ", i), CloseReason: fmt.Sprint("outcome ", i)})
	}
	step.Children = append(step.Children,
		livedata.SnapStep{BeadID: "s.last", Task: true, Status: "closed", Title: "Spec-driven rewrite", CloseReason: "Spec update and regeneration done; source unchanged by regeneration"},
		livedata.SnapStep{BeadID: "s.quiet", Task: true, Status: "closed", Title: "No reason given"},
		livedata.SnapStep{BeadID: "s.open", Task: true, Status: "in_progress", Title: "Still running"})
	ws := livedata.SnapWorkstream{Project: "p", BeadID: "w", Title: "Report templates", NowStep: "s", Steps: []livedata.SnapStep{step}}
	in, _ := overviewInput(ws, "")
	var found map[string]any
	tasks := 0
	for _, e := range in.Evidence {
		if strings.Contains(e.Text, "finished Operator task") {
			tasks++
		}
		if e.ID == "s.last" {
			json.Unmarshal([]byte(e.Text), &found)
		}
		if e.ID == "s.quiet" || e.ID == "s.open" || e.ID == "s.0" {
			t.Fatalf("task with no outcome, a running task or the oldest one reached the reader: %+v", e)
		}
	}
	if found["outcome"] != "Spec update and regeneration done; source unchanged by regeneration" || found["part_of"] != "Regenerate the report templates" {
		t.Fatalf("latest outcome missing: %v", found)
	}
	if tasks != 12 || len(in.Evidence) > 48 {
		t.Fatalf("finished tasks not bounded: %d tasks, %d evidence", tasks, len(in.Evidence))
	}
}

// In production a closed step's tasks are folded off the board: the only record of
// what its work found rides on the step (finished_tasks). The reader must see it, and
// a blocker folded away finished is no outside wait.
func TestOverviewReaderSeesOutcomesFoldedUnderAClosedStep(t *testing.T) {
	ws := livedata.SnapWorkstream{Project: "p", BeadID: "w", Title: "Report templates", Steps: []livedata.SnapStep{
		{BeadID: "w.1", Title: "Regenerate from the spec", Status: "closed", CloseReason: "aligned spec-first", FinishedTasks: []livedata.FinishedTask{
			{BeadID: "w.1.5", Title: "Implement spec", Outcome: "implement-spec done; source unchanged by regeneration"},
			{BeadID: "w.1.4", Title: "Update spec", Outcome: "spec updated"},
		}},
		{BeadID: "w.2", Title: "Ship it", Status: "open", BlockedBy: []string{"w.1.6", "elsewhere"}, DoneBlockers: []string{"w.1.6"}},
	}}
	in, _ := overviewInput(ws, "")
	var found, ship map[string]any
	for _, e := range in.Evidence {
		switch e.ID {
		case "w.1.5":
			json.Unmarshal([]byte(e.Text), &found)
		case "w.2":
			json.Unmarshal([]byte(e.Text), &ship)
		}
	}
	if found["outcome"] != "implement-spec done; source unchanged by regeneration" || found["part_of"] != "Regenerate from the spec" || found["kind"] != "finished Operator task" {
		t.Fatalf("folded outcome missing: %v", found)
	}
	if ship["also_waits_on_beads_outside_this_workstream"] != float64(1) || ship["waits_for"] != nil {
		t.Fatalf("a folded finished blocker reads as a wait: %v", ship)
	}
}

func TestOverviewWaitsPlainlyWhileTheFirstSnapshotLoads(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	s := New(Options{Token: "token", Source: func(context.Context) livedata.Snapshot {
		<-release
		return livedata.Snapshot{Workstreams: []livedata.SnapWorkstream{overviewFixture()}}
	}, OverviewGenerate: func(context.Context, overview.Input) (overview.Recap, error) { return overview.Recap{}, nil }})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("POST", "/api/overview", strings.NewReader(`{"project":"p","bead_id":"w"}`))
		r.Header.Set("Authorization", "Bearer token")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		done <- w
	}()
	select {
	case w := <-done:
		var res overview.Result
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != 200 || res.State != "waiting" || !strings.Contains(res.WaitingFor, "board to finish loading") {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the request hung on the first snapshot load")
	}
}

func TestOverviewStreamsDraftThenRecap(t *testing.T) {
	step, release := make(chan struct{}), make(chan struct{})
	s := New(Options{Token: "token", Source: func(context.Context) livedata.Snapshot {
		return livedata.Snapshot{Workstreams: []livedata.SnapWorkstream{overviewFixture()}}
	}, OverviewGenerate: func(ctx context.Context, in overview.Input) (overview.Recap, error) {
		overview.ProgressOf(ctx)(overview.Recap{Problem: overview.Claim{Sentences: []overview.Sentence{{Text: "Started."}}}})
		close(step)
		<-release
		id := in.Evidence[0].ID
		one := overview.Claim{Sentences: []overview.Sentence{{Text: "Done.", Sources: []string{id}}}}
		return overview.Recap{ProblemType: "feature", Problem: one, ProblemImpact: one, Solution: one, Impact: one, Scope: "Small", ScopeDetail: one, Risk: 1, RiskDetail: one}, nil
	}})
	s.Refresh(context.Background())
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/api/overview", strings.NewReader(`{"project":"p","bead_id":"w","stream":true}`))
	req.Header.Set("Authorization", "Bearer token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	var states []string
	sawDraft := false
	for {
		var res overview.Result
		if dec.Decode(&res) != nil {
			break
		}
		states = append(states, res.State)
		if res.Draft != nil && len(res.Draft.Problem.Sentences) == 1 {
			if !sawDraft {
				<-step
				close(release)
			}
			sawDraft = true
		}
	}
	if !sawDraft || states[len(states)-1] != "ready" {
		t.Fatalf("draft=%v states=%v", sawDraft, states)
	}
}
