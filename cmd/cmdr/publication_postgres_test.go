package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/example-user/commander/internal/api"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestPostgresPlanPublicationCLIUsesCanonicalServiceOnce(t *testing.T) {
	st, dsn := pgtest.Open(t)
	ctx := context.Background()
	home, dir := t.TempDir(), t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_ROLE", "commander-urgent")
	t.Setenv("COMMANDER_MANAGER_ROLE", "")
	if e := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(filepath.Join(dir, ".beads"), 0700); e != nil {
		t.Fatal(e)
	}
	pid, e := st.InsertProject(ctx, store.Project{Name: "publication-cli", Path: dir, Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,parent_id,title,labels,created_at,updated_at) VALUES(?,'root',NULL,'Goal','["kind:objective","kind:workstream"]','now','now'),(?,'track','root','Scope','["kind:objective","kind:track"]','now','now'),(?,'leaf','track','Protected action','["kind:objective"]','now','now'),(?,'card',NULL,'Approve action','["kind:gate","gate-of:leaf","gate-name:scope","verifier:user","approver:user"]','now','now')`, pid, pid, pid, pid); e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_dependencies(project_id,issue_id,depends_on_id,type) VALUES(?,'track','card','blocks'),(?,'leaf','card','blocks')`, pid, pid); e != nil {
		t.Fatal(e)
	}
	if e = st.RegisterCommander(ctx, "commander-urgent", "Urgent"); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"root", "card"} {
		j, e := st.Jurisdiction(ctx, pid, id)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: id, ExpectedOwner: j.CommanderID, ExpectedRevision: j.Revision, NextCommander: "commander-urgent", Reason: "scoped publication CLI"}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = st.SetOrchestrationControl(ctx, 1, "active", "publication CLI fixture"); e != nil {
		t.Fatal(e)
	}
	handler := api.New(api.Options{Token: "publication-test", StateStore: st}).Handler()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == beads.PublicationPath {
			calls.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	for name, value := range map[string]string{"api-url": server.URL, "api-token": "publication-test"} {
		if e = os.WriteFile(filepath.Join(home, name), []byte(value), 0600); e != nil {
			t.Fatal(e)
		}
	}
	reason := "Keep the approval and remove only its redundant ancestor wait"
	for i, dry := range []bool{true, false, false} {
		if e = gatePublishRepair(ctx, dir, "card", "track", []string{"track"}, reason, dry); e != nil {
			t.Fatal(e)
		}
		if calls.Load() != int64(i+1) {
			t.Fatal("typed CLI did not delegate exactly once", calls.Load())
		}
		var parent string
		var comments int
		if e = st.DB().QueryRowContext(ctx, `SELECT COALESCE(parent_id,''),(SELECT COUNT(*) FROM bead_comments WHERE project_id=? AND bead_id='card') FROM bead_tasks WHERE project_id=? AND id='card'`, pid, pid).Scan(&parent, &comments); e != nil {
			t.Fatal(e)
		}
		if dry {
			if parent != "" || comments != 0 {
				t.Fatal("CLI dry run wrote state")
			}
		} else if parent != "track" || comments != 2 {
			t.Fatalf("CLI lost placement or repeated its audit: %s %d", parent, comments)
		}
	}
	// Ordinary publication follows the same service and current-owner boundary.
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,labels,created_at,updated_at) VALUES(?,'normal-card','Second approval','["kind:gate","gate-of:leaf","gate-name:second","verifier:user","approver:user"]','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	j, e := st.Jurisdiction(ctx, pid, "normal-card")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: "normal-card", ExpectedOwner: j.CommanderID, ExpectedRevision: j.Revision, NextCommander: "commander-urgent", Reason: "own the new publication"}); e != nil {
		t.Fatal(e)
	}
	if e = gatePublish(ctx, dir, "normal-card", "track", false); e != nil {
		t.Fatal(e)
	}
	var parent string
	if e = st.DB().QueryRowContext(ctx, `SELECT parent_id FROM bead_tasks WHERE project_id=? AND id='normal-card'`, pid).Scan(&parent); e != nil || parent != "track" {
		t.Fatal("normal scoped publication failed", parent, e)
	}
	server.Close()
	if e = gatePublish(ctx, dir, "normal-card", "track", false); e == nil {
		t.Fatal("missing canonical service silently fell back to local publication")
	}
}

func TestPostgresPlanPublicationCLISavedProposalUsesTypedEndpoint(t *testing.T) {
	st, dsn := pgtest.Open(t)
	ctx := context.Background()
	home, dir := t.TempDir(), t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_ROLE", "commander-urgent")
	t.Setenv("COMMANDER_MANAGER_ROLE", "")
	if e := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(filepath.Join(dir, ".beads"), 0700); e != nil {
		t.Fatal(e)
	}
	pid, e := st.InsertProject(ctx, store.Project{Name: "publication-cli", Path: dir, Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,parent_id,title,labels,created_at,updated_at) VALUES(?,'root',NULL,'Goal','["kind:objective","kind:workstream"]','now','now'),(?,'track','root','Scope','["kind:objective","kind:track"]','now','now'),(?,'leaf','track','Protected action','["kind:objective"]','now','now'),(?,'card',NULL,'Approve action','["kind:gate","gate-of:leaf","gate-name:scope","verifier:user","approver:user"]','now','now')`, pid, pid, pid, pid); e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_dependencies(project_id,issue_id,depends_on_id,type) VALUES(?,'track','card','blocks'),(?,'leaf','card','blocks')`, pid, pid); e != nil {
		t.Fatal(e)
	}
	if e = st.RegisterCommander(ctx, "commander-urgent", "Urgent"); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"root", "card"} {
		j, e := st.Jurisdiction(ctx, pid, id)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: id, ExpectedOwner: j.CommanderID, ExpectedRevision: j.Revision, NextCommander: "commander-urgent", Reason: "scoped publication CLI"}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = st.SetOrchestrationControl(ctx, 1, "active", "publication CLI fixture"); e != nil {
		t.Fatal(e)
	}
	handler := api.New(api.Options{Token: "publication-test", StateStore: st}).Handler()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == beads.PublicationPath {
			calls.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	for name, value := range map[string]string{"api-url": server.URL, "api-token": "publication-test"} {
		if e = os.WriteFile(filepath.Join(home, name), []byte(value), 0600); e != nil {
			t.Fatal(e)
		}
	}

	// Leave the protected work with the urgent Commander and return the gate.
	j, e := st.Jurisdiction(ctx, pid, "card")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: "card", ExpectedOwner: j.CommanderID, ExpectedRevision: j.Revision, NextCommander: "commander", Reason: "separate approval planning"}); e != nil {
		t.Fatal(e)
	}
	document := beads.PlanDocument{Dir: dir, Basis: beads.PlanBasis{RequestID: "typed-publication-cli", Beads: map[string]int64{}}, Publication: &beads.PublicationAction{Gate: "card", Parent: "track", Waiters: []string{"track"}, Reason: "Preserve leaf approval and repair only the redundant wait"}}
	for _, id := range []string{"card", "leaf", "track"} {
		b, e := beads.New().Show(ctx, dir, id)
		if e != nil {
			t.Fatal(e)
		}
		document.Basis.Beads[id] = b.PlanRevision
		document.Basis.JurisdictionRevision = b.JurisdictionRevision
	}
	raw, e := json.Marshal(document)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(home, "publication-plan.json")
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	var out, errOut bytes.Buffer
	if code := runPlan([]string{"propose", "--file", path}, &out, &errOut); code != 0 {
		t.Fatal("typed propose failed", code, out.String(), errOut.String())
	}
	var proposal beads.DoorResponse
	if e = json.Unmarshal(out.Bytes(), &proposal); e != nil || proposal.ProposalID == "" || calls.Load() != 1 {
		t.Fatal("proposal did not use one typed endpoint", proposal, calls.Load(), e)
	}
	out.Reset()
	errOut.Reset()
	if code := runPlan([]string{"apply", "--proposal", proposal.ProposalID}, &out, &errOut); code == 0 {
		t.Fatal("CLI success exit for pending owner agreement")
	}
	out.Reset()
	errOut.Reset()
	if code := runPlan([]string{"get", "--id", proposal.ProposalID}, &out, &errOut); code != 0 || !bytes.Contains(out.Bytes(), []byte("publication")) {
		t.Fatal("saved action not reviewable", code, out.String(), errOut.String())
	}
	for _, owner := range []string{"commander", "commander-urgent"} {
		if _, e = st.AnswerQuestion(ctx, proposal.ProposalID, owner, "Agree to exact publication", true); e != nil {
			t.Fatal(e)
		}
	}
	for i := 0; i < 2; i++ {
		out.Reset()
		errOut.Reset()
		if code := runPlan([]string{"apply", "--proposal", proposal.ProposalID}, &out, &errOut); code != 0 {
			t.Fatal("saved typed apply failed", code, out.String(), errOut.String())
		}
		var result beads.DoorResponse
		if e = json.Unmarshal(out.Bytes(), &result); e != nil || result.Replayed != (i == 1) {
			t.Fatal("typed action replay failed", result, e)
		}
	}
	if calls.Load() != 4 {
		t.Fatal("typed operations used unexpected endpoint count", calls.Load())
	}
	var parent, status string
	var audits int
	if e = st.DB().QueryRow(`SELECT parent_id,status,(SELECT count(*) FROM bead_comments WHERE project_id=? AND bead_id='card') FROM bead_tasks WHERE project_id=? AND id='card'`, pid, pid).Scan(&parent, &status, &audits); e != nil || parent != "track" || status != "open" || audits != 2 {
		t.Fatal("saved action lost approval or repeated audit", parent, status, audits, e)
	}
	server.Close()
	out.Reset()
	errOut.Reset()
	if code := runPlan([]string{"apply", "--proposal", proposal.ProposalID}, &out, &errOut); code == 0 {
		t.Fatal("typed action silently fell back after service stopped")
	}
}
