package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
)

func postAutomaticGate(base string, req beads.AutomaticGateRequest) (beads.AutomaticGateResponse, error) {
	raw, e := json.Marshal(req)
	if e != nil {
		return beads.AutomaticGateResponse{}, e
	}
	r, e := http.NewRequest(http.MethodPost, base+beads.AutomaticGatePath, bytes.NewReader(raw))
	if e != nil {
		return beads.AutomaticGateResponse{}, e
	}
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Set("Content-Type", "application/json")
	response, e := (&http.Client{Timeout: 10 * time.Second}).Do(r)
	if e != nil {
		return beads.AutomaticGateResponse{}, e
	}
	defer response.Body.Close()
	var out beads.AutomaticGateResponse
	e = json.NewDecoder(response.Body).Decode(&out)
	if e == nil && response.StatusCode != http.StatusOK {
		e = fmt.Errorf("HTTP %d: %s", response.StatusCode, out.Error)
	}
	return out, e
}

func TestPostgresPlanAutomaticGateServiceOwnsEvaluationAndFencesStaleResults(t *testing.T) {
	var scenario struct {
		Command string `json:"command"`
		Changed string `json:"changed_command"`
		Passed  string `json:"passed_evidence"`
		Failed  string `json:"failed_evidence"`
	}
	raw, e := os.ReadFile(filepath.Join("..", "..", "tests", "evals", "scenarios", "commander-automatic-gate.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &scenario); e != nil {
		t.Fatal(e)
	}
	st, dsn := pgtest.Open(t)
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	if e = os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
		t.Fatal(e)
	}
	dir := projectDir(t)
	pid, e := st.InsertProject(ctx, store.Project{Name: "automatic-gate", Path: dir, Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,labels,created_at,updated_at) VALUES(?,'root','Release','["kind:objective"]','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,parent_id,title,description,labels,created_at,updated_at) VALUES(?,'auto','root','Tests passed',?,'["kind:gate","gate-of:root","gate-name:tests","verifier:auto"]','now','now')`, pid, scenario.Command); e != nil {
		t.Fatal(e)
	}
	if _, e = st.SetOrchestrationControl(ctx, 1, "active", "automatic gate service test"); e != nil {
		t.Fatal(e)
	}
	var calls atomic.Int32
	var mu sync.Mutex
	mode := "pass"
	var effect func(context.Context) error
	var started, release chan struct{}
	runner := func(ctx context.Context, actualDir, command string) (string, error) {
		calls.Add(1)
		if actualDir != dir || (command != scenario.Command && command != scenario.Changed) {
			return "", fmt.Errorf("service ran an unrecorded command: %s %q", actualDir, command)
		}
		mu.Lock()
		m, fn, start, wait := mode, effect, started, release
		mu.Unlock()
		if fn != nil {
			if e := fn(ctx); e != nil {
				return "", e
			}
		}
		if start != nil {
			close(start)
		}
		if wait != nil {
			select {
			case <-wait:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		if m == "fail" {
			return scenario.Failed, errors.New("failed check")
		}
		return scenario.Passed, nil
	}
	ts := httptest.NewServer(New(Options{Token: testToken, StateStore: st, AutomaticGateRun: runner}).Handler())
	defer ts.Close()
	owner := []string{"COMMANDER_ROLE=commander"}
	req := beads.AutomaticGateRequest{Dir: dir, Bead: "auto", Env: owner}
	for _, env := range [][]string{owner, nil} {
		_, out := postWrite(t, ts.URL, testToken, beads.DoorRequest{Dir: dir, Args: []string{"close", "auto"}, NoWait: true, Env: env})
		if out.Exit == 0 || !strings.Contains(out.Error, "canonical successful check") {
			t.Fatalf("raw passing write fabricated a check: %+v", out)
		}
	}
	for _, args := range [][]string{{"comments", "add", "auto", "gate passed by auto at 1: counterfeit result"}, {"update", "auto", "--add-label", "gate-status:failed"}} {
		_, write := postWrite(t, ts.URL, testToken, beads.DoorRequest{Dir: dir, Args: args, NoWait: true, Env: owner})
		if write.Exit == 0 || !strings.Contains(write.Error, "service-owned") {
			t.Fatalf("caller forged an automatic fact: %+v", write)
		}
	}
	foreign := req
	foreign.Env = []string{"COMMANDER_ROLE=executor-generic"}
	out, e := postAutomaticGate(ts.URL, foreign)
	if e != nil || out.Exit == 0 || calls.Load() != 0 {
		t.Fatalf("foreign executor ran a planning check: %+v calls=%d %v", out, calls.Load(), e)
	}
	// A caller cannot submit a claimed status, command or receipt in this schema.
	body := []byte(fmt.Sprintf(`{"dir":%q,"bead":"auto","status":"passed"}`, dir))
	forged, _ := http.NewRequest(http.MethodPost, ts.URL+beads.AutomaticGatePath, bytes.NewReader(body))
	forged.Header.Set("Authorization", "Bearer "+testToken)
	response, e := http.DefaultClient.Do(forged)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatal("caller provided its own successful outcome", response.StatusCode, calls.Load())
	}
	req.Env = append(owner, "COMMANDER_RUN_ID=999999", "COMMANDER_RUN_SESSION=another-foreground-bead")
	out, e = postAutomaticGate(ts.URL, req)
	if e != nil || out.Exit == 0 || calls.Load() != 0 {
		t.Fatalf("execution context borrowed planning registration: %+v calls=%d %v", out, calls.Load(), e)
	}
	req.Env = owner
	out, e = postAutomaticGate(ts.URL, req)
	if e != nil || out.Exit != 0 || out.Status != "passed" || out.Evidence != scenario.Passed || calls.Load() != 1 {
		t.Fatalf("canonical success failed: %+v calls=%d %v", out, calls.Load(), e)
	}
	var status string
	var failed bool
	var comments, borrowedRuns int
	read := func() {
		t.Helper()
		if e = st.DB().QueryRowContext(ctx, `SELECT status,jsonb_exists(labels,'gate-status:failed') FROM bead_tasks WHERE project_id=? AND id='auto'`, pid).Scan(&status, &failed); e != nil {
			t.Fatal(e)
		}
		if e = st.DB().QueryRowContext(ctx, `SELECT COUNT(*),COUNT(run_id) FROM bead_comments WHERE project_id=? AND bead_id='auto'`, pid).Scan(&comments, &borrowedRuns); e != nil {
			t.Fatal(e)
		}
	}
	read()
	if status != "closed" || failed || comments != 1 || borrowedRuns != 0 {
		t.Fatal("automatic result was misattributed or not atomic", status, failed, comments, borrowedRuns)
	}
	var progressBefore, progressAfter int64
	if e = st.DB().QueryRowContext(ctx, `SELECT progress_seq FROM bead_tasks WHERE project_id=? AND id='auto'`, pid).Scan(&progressBefore); e != nil {
		t.Fatal(e)
	}
	repeated, e := postAutomaticGate(ts.URL, req)
	if e != nil || repeated.Exit != 0 || !repeated.Unchanged {
		t.Fatalf("identical observation fabricated a new advance: %+v %v", repeated, e)
	}
	read()
	if e = st.DB().QueryRowContext(ctx, `SELECT progress_seq FROM bead_tasks WHERE project_id=? AND id='auto'`, pid).Scan(&progressAfter); e != nil || progressAfter != progressBefore || comments != 1 {
		t.Fatal("unchanged check reset the progress clock", progressBefore, progressAfter, comments, e)
	}
	var author, kind string
	if e = st.DB().QueryRowContext(ctx, `SELECT author,fact_kind FROM bead_comments WHERE project_id=? AND bead_id='auto'`, pid).Scan(&author, &kind); e != nil || author != "auto" || kind != "automatic-gate" {
		t.Fatal("checker evidence was misattributed", author, kind, e)
	}
	_, write := postWrite(t, ts.URL, testToken, beads.DoorRequest{Dir: dir, Args: []string{"update", "auto", "--description", scenario.Changed}, NoWait: true, Env: owner})
	if write.Exit == 0 || !strings.Contains(write.Error, "reopen") {
		t.Fatalf("a passing gate silently changed its definition: %+v", write)
	}
	mu.Lock()
	mode = "fail"
	mu.Unlock()
	out, e = postAutomaticGate(ts.URL, req)
	if e != nil || out.Exit != 0 || out.Status != "failed" || !strings.Contains(out.Evidence, scenario.Failed) {
		t.Fatalf("canonical failure was not recorded: %+v %v", out, e)
	}
	read()
	if status != "open" || !failed || comments != 2 {
		t.Fatal("failed check kept the gate passed", status, failed, comments)
	}
	// Acquiring the same graph lock inside the runner proves evaluation occurs
	// outside the write transaction. A changed definition then rejects all writes.
	mu.Lock()
	mode = "pass"
	effect = func(parent context.Context) error {
		editCtx, cancel := context.WithTimeout(parent, 2*time.Second)
		defer cancel()
		return st.WithTx(editCtx, func(q store.Querier) error {
			if _, e := q.ExecContext(editCtx, `SELECT pg_advisory_xact_lock(?)`, pid); e != nil {
				return e
			}
			_, e := q.ExecContext(editCtx, `UPDATE bead_tasks SET description=? WHERE project_id=? AND id='auto'`, scenario.Changed, pid)
			return e
		})
	}
	mu.Unlock()
	out, e = postAutomaticGate(ts.URL, req)
	if e != nil || out.Exit == 0 || !strings.Contains(out.Error, "changed") {
		t.Fatalf("check held the graph lock or accepted an old definition: %+v %v", out, e)
	}
	read()
	if status != "open" || !failed || comments != 2 {
		t.Fatal("stale result overwrote the changed gate", status, failed, comments)
	}
	// A second service models restart overlap. A late failure from the old check
	// must not reopen a newer successful result on the identical definition.
	mu.Lock()
	mode = "fail"
	effect = nil
	started = make(chan struct{})
	release = make(chan struct{})
	start, finish := started, release
	mu.Unlock()
	var finishOnce sync.Once
	releaseOld := func() { finishOnce.Do(func() { close(finish) }) }
	defer releaseOld()
	oldDone := make(chan beads.AutomaticGateResponse, 1)
	oldErr := make(chan error, 1)
	go func() { r, e := postAutomaticGate(ts.URL, req); oldDone <- r; oldErr <- e }()
	select {
	case <-start:
	case <-time.After(5 * time.Second):
		t.Fatal("old check did not start")
	}
	fresh := httptest.NewServer(New(Options{Token: testToken, StateStore: st, AutomaticGateRun: func(context.Context, string, string) (string, error) { return scenario.Passed, nil }}).Handler())
	defer fresh.Close()
	newer, e := postAutomaticGate(fresh.URL, req)
	if e != nil || newer.Exit != 0 || newer.Status != "passed" {
		releaseOld()
		t.Fatalf("new service could not commit its check: %+v %v", newer, e)
	}
	releaseOld()
	out = <-oldDone
	e = <-oldErr
	if e != nil || out.Exit == 0 || !strings.Contains(out.Error, "changed") {
		t.Fatalf("late failure was not fenced: %+v %v", out, e)
	}
	read()
	if status != "closed" || failed || comments != 3 {
		t.Fatal("late failure reopened the newer passing gate", status, failed, comments)
	}
}

type joinedGateContext struct {
	context.Context
	joined chan struct{}
	once   sync.Once
	never  chan struct{}
}

func (c *joinedGateContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.joined) })
	return c.never
}
func TestAutomaticGateConcurrentRequestsShareOneEvaluation(t *testing.T) {
	var flights automaticGateFlights
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	results := make(chan beads.AutomaticGateResponse, 2)
	run := func(chan struct{}) beads.AutomaticGateResponse {
		calls.Add(1)
		close(started)
		<-release
		return beads.AutomaticGateResponse{Bead: "gate", Status: "passed", Evidence: "one actual evaluation"}
	}
	go func() { results <- flights.do(context.Background(), "same definition and versions", run) }()
	<-started
	joined := &joinedGateContext{Context: context.Background(), joined: make(chan struct{}), never: make(chan struct{})}
	go func() {
		results <- flights.do(joined, "same definition and versions", func(chan struct{}) beads.AutomaticGateResponse {
			calls.Add(1)
			return beads.AutomaticGateResponse{Exit: 1, Error: "duplicate execution"}
		})
	}()
	select {
	case <-joined.joined:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("concurrent check did not join the existing evaluation")
	}
	close(release)
	a, b := <-results, <-results
	if calls.Load() != 1 || !reflect.DeepEqual(a, b) || a.Status != "passed" {
		t.Fatal("concurrent callers reran the check", calls.Load(), a, b)
	}
}

func TestAutomaticGateRequestCancellationDoesNotPublishFailure(t *testing.T) {
	st, dsn := pgtest.Open(t)
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	if e := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	dir := projectDir(t)
	pid, e := st.InsertProject(ctx, store.Project{Name: "cancel-auto", Path: dir, Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,description,labels,created_at,updated_at) VALUES(?,'auto','Tests passed','fixture-check','["kind:gate","verifier:auto"]','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	if _, e = st.SetOrchestrationControl(ctx, 1, "active", "cancel automatic check"); e != nil {
		t.Fatal(e)
	}
	started, ended := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(New(Options{Token: testToken, StateStore: st, AutomaticGateRun: func(ctx context.Context, _, _ string) (string, error) {
		close(started)
		<-ctx.Done()
		close(ended)
		return "request was cancelled", ctx.Err()
	}}).Handler())
	defer server.Close()
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	raw, _ := json.Marshal(beads.AutomaticGateRequest{Dir: dir, Bead: "auto", Env: []string{"COMMANDER_ROLE=commander"}})
	request, _ := http.NewRequestWithContext(requestCtx, http.MethodPost, server.URL+beads.AutomaticGatePath, bytes.NewReader(raw))
	request.Header.Set("Authorization", "Bearer "+testToken)
	done := make(chan struct{})
	go func() {
		response, _ := http.DefaultClient.Do(request)
		if response != nil {
			response.Body.Close()
		}
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("check never started")
	}
	cancel()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled requester left its check running")
	}
	<-done
	var status string
	var rows int
	if e = st.DB().QueryRowContext(ctx, `SELECT status FROM bead_tasks WHERE project_id=? AND id='auto'`, pid).Scan(&status); e != nil || status != "open" {
		t.Fatal("cancellation changed a gate outcome", status, e)
	}
	if e = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM bead_comments WHERE project_id=? AND bead_id='auto'`, pid).Scan(&rows); e != nil || rows != 0 {
		t.Fatal("cancellation was published as a failed check", rows, e)
	}
}
