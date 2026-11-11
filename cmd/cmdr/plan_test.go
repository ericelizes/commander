package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/example-user/commander/internal/api"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/supervision"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPlanRejectsActorOverrideAndAmbiguousInputBeforeSubmission(t *testing.T) {
	for _, input := range []string{
		`{"dir":"/project","basis":{"jurisdiction_revision":1},"publication":{"gate":"gate","parent":"parent"},"ops":[["update","work","--title","x"]]}`,
		`{"dir":"/project","actor":"commander","basis":{"jurisdiction_revision":1},"ops":[["update","work","--title","x"]]}`,
		`{"dir":"/project","basis":{"jurisdiction_revision":1},"ops":[["update","work","--title","x"]]} {"ops":[]}`,
		`{"basis":{"jurisdiction_revision":1},"ops":[["update","work","--title","x"]]}`,
	} {
		t.Run(input, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "plan.json")
			if e := os.WriteFile(path, []byte(input), 0600); e != nil {
				t.Fatal(e)
			}
			var out, err bytes.Buffer
			if code := runPlan([]string{"apply", "--file", path}, &out, &err); code != 2 {
				t.Fatalf("invalid request reached submission: code=%d error=%s", code, err.String())
			}
		})
	}
}

func TestPostgresPlanCLIProposesAndAppliesSavedPayload(t *testing.T) {
	st, dsn := pgtest.Open(t)
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_ROLE", "commander")
	if e := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	if e := os.Mkdir(filepath.Join(dir, ".beads"), 0700); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	pid, e := st.InsertProject(ctx, store.Project{Name: "cli-plan", Path: dir, Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) VALUES(?,'ordinary','Ordinary','now','now'),(?,'urgent','Urgent','now','now')`, pid, pid); e != nil {
		t.Fatal(e)
	}
	if e = st.RegisterCommander(ctx, "commander-urgent", "Urgent"); e != nil {
		t.Fatal(e)
	}
	if _, e = st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: "urgent", ExpectedOwner: "commander", ExpectedRevision: 1, NextCommander: "commander-urgent", Reason: "urgent scope"}); e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(api.New(api.Options{Token: "plan-test", StateStore: st}).Handler())
	defer server.Close()
	for name, value := range map[string]string{"api-url": server.URL, "api-token": "plan-test"} {
		if e = os.WriteFile(filepath.Join(home, name), []byte(value), 0600); e != nil {
			t.Fatal(e)
		}
	}
	payload, e := json.Marshal(map[string]any{"dir": dir, "basis": beads.PlanBasis{RequestID: "saved-cli-plan", JurisdictionRevision: 2, Beads: map[string]int64{"ordinary": 1, "urgent": 1}}, "ops": [][]string{{"update", "ordinary", "--title", "Agreed ordinary"}, {"update", "urgent", "--title", "Agreed urgent"}}})
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(home, "plan.json")
	if e = os.WriteFile(path, payload, 0600); e != nil {
		t.Fatal(e)
	}
	var out, err bytes.Buffer
	if code := runPlan([]string{"propose", "--file", path}, &out, &err); code != 0 {
		t.Fatalf("propose code=%d: %s", code, err.String())
	}
	var proposal beads.DoorResponse
	if e = json.Unmarshal(out.Bytes(), &proposal); e != nil || proposal.ProposalID == "" {
		t.Fatal("missing CLI proposal", e)
	}
	for _, owner := range []string{"commander", "commander-urgent"} {
		if _, e = st.AnswerQuestion(ctx, proposal.ProposalID, owner, "Agree to the saved edits", true); e != nil {
			t.Fatal(e)
		}
	}
	out.Reset()
	err.Reset()
	if code := runPlan([]string{"get", "--id", proposal.ProposalID}, &out, &err); code != 0 {
		t.Fatalf("get code=%d: %s", code, err.String())
	}
	for i := 0; i < 2; i++ {
		out.Reset()
		err.Reset()
		if code := runPlan([]string{"apply", "--proposal", proposal.ProposalID}, &out, &err); code != 0 {
			t.Fatalf("saved apply code=%d: %s", code, err.String())
		}
		var result beads.DoorResponse
		if e = json.Unmarshal(out.Bytes(), &result); e != nil || result.Replayed != (i == 1) || result.Applied != 2 {
			t.Fatalf("saved apply lost receipt: %+v %v", result, e)
		}
	}
}

func TestPostgresPlanHumanDecisionCLIUsesCurrentScopeWithoutCompletingResumeWork(t *testing.T) {
	for _, kind := range []string{"automatic-gate", "resume-hold"} {
		t.Run(kind, func(t *testing.T) {
			stubMail(t)
			st, dsn := pgtest.Open(t)
			home := t.TempDir()
			t.Setenv("COMMANDER_HOME", home)
			t.Setenv("COMMANDER_ROLE", "commander")
			if e := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
				t.Fatal(e)
			}
			dir := t.TempDir()
			if e := os.Mkdir(filepath.Join(dir, ".beads"), 0700); e != nil {
				t.Fatal(e)
			}
			ctx := context.Background()
			pid, e := st.InsertProject(ctx, store.Project{Name: "decision-cli", Path: dir, Type: "knowledge", Status: "active"})
			if e != nil {
				t.Fatal(e)
			}
			labels := `["gate","kind:gate","approver:user","verifier:user","needs-user"]`
			choice := "Approve"
			if kind == "resume-hold" {
				labels = `["kind:objective","hold","needs-user"]`
				choice = "Resume"
			}
			if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,description,labels,created_at,updated_at) VALUES(?,'decision','Approve current scope','The exact work being approved',?::jsonb,'now','now')`, pid, labels); e != nil {
				t.Fatal(e)
			}
			if _, e = st.SetOrchestrationControl(ctx, 1, "active", "decision CLI test"); e != nil {
				t.Fatal(e)
			}
			if kind == "resume-hold" {
				card := `{"question":"Resume this held work?","options":[{"label":"Resume","approves":true},{"label":"Keep held"}]}`
				if e = st.UpsertNeedsYou(ctx, store.NeedsYou{ProjectID: pid, BeadID: "decision", Ask: "Resume held work?", Decision: card, Since: 1}); e != nil {
					t.Fatal(e)
				}
			}
			server := httptest.NewServer(api.New(api.Options{Token: "decision-test", StateStore: st}).Handler())
			defer server.Close()
			for name, value := range map[string]string{"api-url": server.URL, "api-token": "decision-test"} {
				if e = os.WriteFile(filepath.Join(home, name), []byte(value), 0600); e != nil {
					t.Fatal(e)
				}
			}
			oldStore, oldBeads := needsYouStoreFn, newBeadsClientFn
			needsYouStoreFn = func() (*store.Store, func(), error) { return st, func() {}, nil }
			newBeadsClientFn = func() beads.Client { return beads.New() }
			t.Cleanup(func() { needsYouStoreFn = oldStore; newBeadsClientFn = oldBeads })
			stdout, stderr, output := withTempOutputFiles(t)
			t.Setenv("COMMANDER_ROLE", "executor-forbidden")
			if code := runInternalResolveNeeds([]string{"--project", "decision-cli", "--bead", "decision", "--action", "decide", "--choice", choice, "--revision", "1"}, stdout, stderr); code == 0 {
				t.Fatal("execution agent manufactured a human answer")
			}
			if recorded, e := st.GetNeedsResolution(ctx, pid, "decision"); e != nil || recorded != nil {
				t.Fatalf("refused actor left a human journal: %+v %v", recorded, e)
			}
			t.Setenv("COMMANDER_ROLE", "commander")
			if code := runInternalResolveNeeds([]string{"--project", "decision-cli", "--bead", "decision", "--action", "decide", "--choice", choice, "--revision", "1"}, stdout, stderr); code != 0 {
				_, errOut := output()
				t.Fatalf("typed answer failed: %d %s", code, errOut)
			}
			answer, e := st.GetNeedsResolution(ctx, pid, "decision")
			if e != nil || answer == nil || !answer.Approved || answer.Manager != "commander" {
				t.Fatalf("typed approval or current planning owner was lost: %+v %v", answer, e)
			}
			var status string
			if e = st.DB().QueryRowContext(ctx, `SELECT status FROM bead_tasks WHERE project_id=? AND id='decision'`, pid).Scan(&status); e != nil {
				t.Fatal(e)
			}
			if kind == "automatic-gate" && status != "closed" {
				t.Fatal("automatic gate approval not applied", status)
			}
			if kind == "resume-hold" {
				if status == "closed" {
					t.Fatal("resuming a hold falsely completed the work")
				}
				if e = beads.New().SetLabel(ctx, dir, "decision", "hold", false); e != nil {
					t.Fatal("recorded resume could not release its hold", e)
				}
			}
		})
	}
}

func TestPostgresPlanSavedHumanAnswerResumesAfterJurisdictionHandoff(t *testing.T) {
	var scenario struct {
		AnswerID  string `json:"answer_id"`
		Choice    string `json:"choice"`
		NextOwner string `json:"next_owner"`
		Prompt    string `json:"expected_prompt"`
	}
	raw, e := os.ReadFile(filepath.Join("..", "..", "tests", "evals", "scenarios", "commander-saved-answer-handoff.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &scenario); e != nil {
		t.Fatal(e)
	}
	mailer := stubMail(t)
	mailStore, _, _, cleanup, mailErr := mailDeps()
	if mailErr != nil {
		t.Fatal(mailErr)
	}
	defer cleanup()
	if e = mailStore.UpsertAmAgent(context.Background(), amKey, scenario.NextOwner, "UrgentFox"); e != nil {
		t.Fatal(e)
	}
	st, dsn := pgtest.Open(t)
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_ROLE", "commander")
	if e = os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	if e = os.Mkdir(filepath.Join(dir, ".beads"), 0700); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	pid, e := st.InsertProject(ctx, store.Project{Name: "saved-answer", Path: dir, Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,description,labels,created_at,updated_at) VALUES(?,'gate','Release approved','The existing scope','["gate","kind:gate","approver:user","verifier:user","needs-user"]','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	if _, e = st.SetOrchestrationControl(ctx, 1, "active", "saved answer handoff"); e != nil {
		t.Fatal(e)
	}
	if e = st.RecordPitch(ctx, pid, "gate", "Release?", `{"question":"Release?","options":[{"label":"Approve","approves":true}]}`, nil); e != nil {
		t.Fatal(e)
	}
	original := store.NeedsResolution{ProjectID: pid, BeadID: "gate", Generation: "1", AnswerID: scenario.AnswerID, Action: "decide", Choice: scenario.Choice, GateChoice: beads.GateApprove, Approved: true, Manager: "commander"}
	if e = st.SaveNeedsResolution(ctx, original, "Approve"); e != nil {
		t.Fatal(e)
	}
	if e = st.RegisterCommander(ctx, scenario.NextOwner, "Urgent owner"); e != nil {
		t.Fatal(e)
	}
	if _, e = st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: "gate", ExpectedOwner: "commander", ExpectedRevision: 1, NextCommander: scenario.NextOwner, Reason: "handoff while answer application was interrupted"}); e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(api.New(api.Options{Token: "saved-answer-test", StateStore: st}).Handler())
	defer server.Close()
	for name, value := range map[string]string{"api-url": server.URL, "api-token": "saved-answer-test"} {
		if e = os.WriteFile(filepath.Join(home, name), []byte(value), 0600); e != nil {
			t.Fatal(e)
		}
	}
	oldStore, oldBeads := needsYouStoreFn, newBeadsClientFn
	needsYouStoreFn = func() (*store.Store, func(), error) { return st, func() {}, nil }
	newBeadsClientFn = func() beads.Client { return beads.New() }
	t.Cleanup(func() { needsYouStoreFn = oldStore; newBeadsClientFn = oldBeads })
	deliveries := 0
	worker := supervision.Worker{Store: st, Mode: "active", Deliver: func(ctx context.Context, role, prompt string) error {
		deliveries++
		if role != scenario.NextOwner || !strings.Contains(prompt, scenario.Prompt) {
			t.Fatalf("saved answer routed incorrectly: role=%s prompt=%s", role, prompt)
		}
		var epoch int64
		var request string
		if e = st.DB().QueryRowContext(ctx, `SELECT epoch,request_id FROM bead_supervision WHERE project_id=? AND bead_id='gate'`, pid).Scan(&epoch, &request); e != nil {
			return e
		}
		if e = st.AcknowledgeSupervision(ctx, pid, "gate", epoch, role, request); e != nil {
			return e
		}
		t.Setenv("COMMANDER_ROLE", role)
		stdout, stderr, output := withTempOutputFiles(t)
		if code := runInternalResolveNeeds([]string{"--project", "saved-answer", "--bead", "gate", "--action", "decide", "--resume-only"}, stdout, stderr); code != 0 {
			_, errOut := output()
			t.Fatalf("saved answer resume failed: %d %s", code, errOut)
		}
		return nil
	}}
	if e = worker.Pass(ctx); e != nil {
		t.Fatal(e)
	}
	if deliveries != 1 {
		t.Fatalf("saved answer delivery count=%d", deliveries)
	}
	if len(mailer.sent) != 1 || len(mailer.sent[0].to) != 1 || mailer.sent[0].to[0] != "UrgentFox" {
		t.Fatalf("saved answer mailed historical owner instead of current owner: %+v", mailer.sent)
	}
	saved, e := st.GetNeedsResolution(ctx, pid, "gate")
	if e != nil || saved == nil || saved.AnswerID != original.AnswerID || saved.Choice != original.Choice || saved.Manager != original.Manager || !saved.Mailed {
		t.Fatalf("resuming rewrote or lost original answer: %+v %v", saved, e)
	}
	var pending bool
	var status string
	if e = st.DB().QueryRowContext(ctx, `SELECT commander_pending_human_answer(project_id,id),status FROM bead_tasks WHERE project_id=? AND id='gate'`, pid).Scan(&pending, &status); e != nil || pending || status != "closed" {
		t.Fatal("saved answer did not finish", pending, status, e)
	}
}

func TestPostgresPlanNoPRWaiverTypedCommandsUseCurrentDefinition(t *testing.T) {
	for _, action := range []string{"pass", "rm", "apply"} {
		t.Run(action, func(t *testing.T) {
			stubMail(t)
			st, dsn := pgtest.Open(t)
			home := t.TempDir()
			t.Setenv("COMMANDER_HOME", home)
			t.Setenv("COMMANDER_ROLE", "commander")
			if e := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
				t.Fatal(e)
			}
			dir := t.TempDir()
			if e := os.Mkdir(filepath.Join(dir, ".beads"), 0700); e != nil {
				t.Fatal(e)
			}
			ctx := context.Background()
			pid, e := st.InsertProject(ctx, store.Project{Name: "no-pr-cli", Path: dir, Type: "knowledge", Status: "active"})
			if e != nil {
				t.Fatal(e)
			}
			// Work stays parked throughout this test. Passing or replacing a release
			// gate must not imply an execution started or clear the existing pause.
			if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,status,labels,created_at,updated_at) VALUES(?,'stage','Deploy on the VM','deferred','["kind:objective"]','now','now')`, pid); e != nil {
				t.Fatal(e)
			}
			if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,parent_id,title,description,labels,created_at,updated_at) VALUES(?,'release','stage','Deployment released','The agreed VM deployment is live','["kind:gate","gate-of:stage","gate-name:merged","verifier:operator"]','now','now')`, pid); e != nil {
				t.Fatal(e)
			}
			if _, e = st.SetOrchestrationControl(ctx, 1, "active", "typed no-PR flow"); e != nil {
				t.Fatal(e)
			}
			server := httptest.NewServer(api.New(api.Options{Token: "no-pr-cli", StateStore: st}).Handler())
			defer server.Close()
			for name, value := range map[string]string{"api-url": server.URL, "api-token": "no-pr-cli"} {
				if e = os.WriteFile(filepath.Join(home, name), []byte(value), 0600); e != nil {
					t.Fatal(e)
				}
			}
			oldStore, oldBeads := needsYouStoreFn, newBeadsClientFn
			needsYouStoreFn = func() (*store.Store, func(), error) { return st, func() {}, nil }
			newBeadsClientFn = func() beads.Client { return beads.New() }
			t.Cleanup(func() { needsYouStoreFn = oldStore; newBeadsClientFn = oldBeads })
			var result any
			switch action {
			case "pass":
				result, e = gatePass(ctx, st, "no-pr-cli", "stage", "merged", "The user approved this VM deployment", "", noPRWhy)
			case "rm":
				result, e = gateRemove(ctx, st, "no-pr-cli", "stage", "merged", noPRWhy, "The user approved this VM deployment")
			case "apply":
				result, e = gateApply(ctx, st, "no-pr-cli", "stage", "investigation", templateArgs{}, noPRWhy, "The user approved this VM deployment")
			}
			if e != nil {
				t.Fatalf("typed %s failed across real API: %v (%+v)", action, e, result)
			}
			var status, stageStatus string
			var notes int
			if e = st.DB().QueryRowContext(ctx, `SELECT status FROM bead_tasks WHERE project_id=? AND id='release'`, pid).Scan(&status); e != nil || status != "closed" {
				t.Fatalf("typed %s did not complete its old gate: %s %v", action, status, e)
			}
			if e = st.DB().QueryRowContext(ctx, `SELECT status FROM bead_tasks WHERE project_id=? AND id='stage'`, pid).Scan(&stageStatus); e != nil || stageStatus != "deferred" {
				t.Fatal("waiver resumed parked work", stageStatus, e)
			}
			if e = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM bead_comments WHERE project_id=? AND bead_id='release' AND text LIKE 'merge rule waived for a ship with no PR by %'`, pid).Scan(&notes); e != nil || notes < 1 {
				t.Fatal("waiver authority was not recorded", notes, e)
			}
		})
	}
}

func TestPostgresPlanAutomaticGateCLIUsesServiceExactlyOnce(t *testing.T) {
	st, dsn := pgtest.Open(t)
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_ROLE", "commander")
	if e := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	if e := os.Mkdir(filepath.Join(dir, ".beads"), 0700); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	pid, e := st.InsertProject(ctx, store.Project{Name: "automatic-cli", Path: dir, Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,status,labels,created_at,updated_at) VALUES(?,'stage','Release','deferred','["kind:objective"]','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,parent_id,title,description,labels,created_at,updated_at) VALUES(?,'auto','stage','Tests passed','fixture-check','["kind:gate","gate-of:stage","gate-name:tests","verifier:auto"]','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	if _, e = st.SetOrchestrationControl(ctx, 1, "active", "typed automatic gate test"); e != nil {
		t.Fatal(e)
	}
	var local, remote atomic.Int32
	var fail atomic.Bool
	previous := gateRunFn
	gateRunFn = func(context.Context, string, string) (string, error) {
		local.Add(1)
		return "local checker must not run", errors.New("local fallback used")
	}
	t.Cleanup(func() { gateRunFn = previous })
	server := httptest.NewServer(api.New(api.Options{Token: "automatic-cli", StateStore: st, AutomaticGateRun: func(_ context.Context, actualDir, command string) (string, error) {
		remote.Add(1)
		if actualDir != dir || command != "fixture-check" {
			return "", errors.New("not the stored check")
		}
		if fail.Load() {
			return "canonical failure", errors.New("failed")
		}
		return "canonical success", nil
	}}).Handler())
	defer server.Close()
	for name, value := range map[string]string{"api-url": server.URL, "api-token": "automatic-cli"} {
		if e = os.WriteFile(filepath.Join(home, name), []byte(value), 0600); e != nil {
			t.Fatal(e)
		}
	}
	oldStore, oldBeads := needsYouStoreFn, newBeadsClientFn
	needsYouStoreFn = func() (*store.Store, func(), error) { return st, func() {}, nil }
	newBeadsClientFn = func() beads.Client { return beads.New() }
	t.Cleanup(func() { needsYouStoreFn = oldStore; newBeadsClientFn = oldBeads })
	for i, want := range []string{"passed", "failed"} {
		fail.Store(i == 1)
		result, e := gateCheck(ctx, st, "automatic-cli", "stage", "tests")
		if e != nil {
			t.Fatalf("typed automatic check failed: %v", e)
		}
		raw, _ := json.Marshal(result)
		if !strings.Contains(string(raw), `"status":"`+want+`"`) {
			t.Fatalf("typed CLI lost the canonical outcome: %s", raw)
		}
		if local.Load() != 0 || remote.Load() != int32(i+1) {
			t.Fatalf("check ran more than once: local=%d service=%d", local.Load(), remote.Load())
		}
	}
	var stage string
	if e = st.DB().QueryRowContext(ctx, `SELECT status FROM bead_tasks WHERE project_id=? AND id='stage'`, pid).Scan(&stage); e != nil || stage != "deferred" {
		t.Fatal("checking a gate resumed parked work", stage, e)
	}
}
