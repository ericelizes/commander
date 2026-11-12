package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/example-user/commander/internal/api"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
)

func TestPostgresPlanDecisionReconciliationCLIUsesCurrentOwnerWithoutApproving(t *testing.T) {
	var scenario struct {
		Answer  string `json:"source_answer_id"`
		Request string `json:"request_id"`
		Ask     string `json:"ask"`
		Reason  string `json:"reason"`
	}
	scenarioRaw, e := os.ReadFile(filepath.Join("..", "..", "tests", "evals", "scenarios", "commander-decision-reconciliation.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(scenarioRaw, &scenario); e != nil || scenario.Answer == "" || scenario.Request == "" {
		t.Fatal("missing reconciliation scenario", e)
	}
	st, dsn := pgtest.Open(t)
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_ROLE", "commander")
	for _, key := range []string{"COMMANDER_RUN_ID", "COMMANDER_RUN_SESSION", "COMMANDER_MANAGER_ROLE"} {
		t.Setenv(key, "")
	}
	if e := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	if e := os.Mkdir(filepath.Join(dir, ".beads"), 0700); e != nil {
		t.Fatal(e)
	}
	pid, e := st.InsertProject(ctx, store.Project{Name: "decision-reconciliation-cli", Path: dir, Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,description,labels,created_at,updated_at) VALUES(?,'held','Release bounded work','Original release scope','["kind:objective","hold","needs-user"]','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	d := &store.Decision{Question: "Resume original work?", Options: []store.DecisionOption{{Label: "Resume", Approves: true}, {Label: "Keep held"}}}
	if e = st.RecordPitch(ctx, pid, "held", "Resume original work?", d.JSON(), nil); e != nil {
		t.Fatal(e)
	}
	saved := store.NeedsResolution{ProjectID: pid, BeadID: "held", Generation: "1", AnswerID: scenario.Answer, Action: "decide", Choice: "Resume", Approved: true, InputPlanRevision: 1, Manager: "commander"}
	if e = st.SaveNeedsResolution(ctx, saved, "Resume"); e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().Exec(`UPDATE bead_tasks SET description='Expanded release scope' WHERE project_id=? AND id='held'`, pid); e != nil {
		t.Fatal(e)
	}
	if _, e = st.SetOrchestrationControl(ctx, 1, "active", "decision recovery test"); e != nil {
		t.Fatal(e)
	}
	if e = st.RegisterCommander(ctx, "commander-urgent", "Urgent"); e != nil {
		t.Fatal(e)
	}
	if _, e = st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: "held", ExpectedOwner: "commander", ExpectedRevision: 1, NextCommander: "commander-urgent", Reason: "current owner handles recovery"}); e != nil {
		t.Fatal(e)
	}
	var calls atomic.Int64
	handler := api.New(api.Options{Token: "reconciliation-test", StateStore: st}).Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == api.DecisionReconciliationPath {
			calls.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	for name, value := range map[string]string{"api-url": server.URL, "api-token": "reconciliation-test"} {
		if e = os.WriteFile(filepath.Join(home, name), []byte(value), 0600); e != nil {
			t.Fatal(e)
		}
	}
	var out, errOut bytes.Buffer
	if code := runDecision([]string{"inspect", "--project-id", strconv.FormatInt(pid, 10), "--bead", "held"}, &out, &errOut); code != 0 {
		t.Fatal(code, errOut.String())
	}
	var view struct {
		Revision     int64  `json:"plan_revision"`
		Jurisdiction int64  `json:"jurisdiction_revision"`
		Generation   string `json:"generation"`
		Owner        string `json:"commander"`
	}
	if e = json.Unmarshal(out.Bytes(), &view); e != nil || view.Owner != "commander-urgent" {
		t.Fatal(view, e)
	}
	req := store.DecisionReconciliation{ProjectID: pid, BeadID: "held", RequestID: scenario.Request, PlanRevision: view.Revision, JurisdictionRevision: view.Jurisdiction, Generation: view.Generation, AnswerID: saved.AnswerID, Reason: scenario.Reason, Ask: scenario.Ask, Decision: &store.Decision{Question: "Resume the expanded scope?", Options: d.Options}}
	raw, _ := json.Marshal(req)
	path := filepath.Join(home, "reconcile.json")
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	// Both an old owner and inherited execution attribution must fail remotely.
	out.Reset()
	errOut.Reset()
	if code := runDecision([]string{"reconcile", "--file", path}, &out, &errOut); code == 0 {
		t.Fatal("old Commander reconciled foreign scope")
	}
	t.Setenv("COMMANDER_ROLE", "commander-urgent")
	t.Setenv("COMMANDER_RUN_SESSION", "executor-session")
	out.Reset()
	errOut.Reset()
	if code := runDecision([]string{"reconcile", "--file", path}, &out, &errOut); code == 0 {
		t.Fatal("executor attribution reconciled decision")
	}
	t.Setenv("COMMANDER_RUN_SESSION", "")
	for i := 0; i < 2; i++ {
		out.Reset()
		errOut.Reset()
		if code := runDecision([]string{"reconcile", "--file", path}, &out, &errOut); code != 0 {
			t.Fatal(code, errOut.String())
		}
		var result store.DecisionReconciliationResult
		if e = json.Unmarshal(out.Bytes(), &result); e != nil || result.Version != 2 || result.Replayed != (i == 1) {
			t.Fatal(result, e)
		}
	}
	if calls.Load() != 4 {
		t.Fatal("CLI did not delegate exactly once", calls.Load())
	}
	// The original approval is superseded, even though its entire immutable
	// payload remains present in the journal.
	before, e := st.ListNeedsResolutions(ctx, pid, "held")
	if e != nil || len(before) != 1 || before[0].AnswerID != saved.AnswerID || before[0].State != store.ResolutionSuperseded {
		t.Fatal(before, e)
	}
	server.Close()
	out.Reset()
	errOut.Reset()
	if code := runDecision([]string{"reconcile", "--file", path}, &out, &errOut); code == 0 {
		t.Fatal("unavailable service used a local write fallback")
	}
}

func TestDecisionReconciliationRejectsFileAttribution(t *testing.T) {
	for _, raw := range []string{`{"project_id":1,"actor":"commander"}`, `{"project_id":1,"env":["COMMANDER_ROLE=commander"]}`, `{"project_id":1} {"project_id":2}`} {
		path := filepath.Join(t.TempDir(), "invalid.json")
		if e := os.WriteFile(path, []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
		var out, errOut bytes.Buffer
		if code := runDecision([]string{"reconcile", "--file", path}, &out, &errOut); code != 2 {
			t.Fatal("file attribution reached service", code, errOut.String())
		}
	}
}
