package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/api"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
)

func TestCommanderPreparesOrdinaryLeafThroughPlanThenSchedulerClaimsIt(t *testing.T) {
	st, dsn := pgtest.Open(t)
	home := t.TempDir()
	dir := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_ROLE", "commander")
	if err := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".beads"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pid, err := st.InsertProject(ctx, store.Project{Name: "planning-ready", Path: dir, Type: "knowledge", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB().Exec(`INSERT INTO bead_tasks(project_id,id,parent_id,title,labels,created_at,updated_at) VALUES(?,'goal',NULL,'Agreed read-only investigation','["kind:objective"]','now','now'),(?,'work','goal','Inspect the failure','["kind:task"]','now','now'),(?,'held',NULL,'Existing human hold','["hold"]','now','now')`, pid, pid, pid); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.New(api.Options{Token: "ready-plan-test", StateStore: st}).Handler())
	defer server.Close()
	for name, value := range map[string]string{"api-url": server.URL, "api-token": "ready-plan-test"} {
		if err = os.WriteFile(filepath.Join(home, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out, stderr bytes.Buffer
	args := []string{"ready", "--project-id", strconv.FormatInt(pid, 10), "--bead", "work", "--revision", "1", "--jurisdiction-revision", "1"}
	if code := runExecution(args, &out, &stderr); code != 1 || !strings.Contains(stderr.String(), "cmdr plan apply") || !strings.Contains(stderr.String(), "--acceptance") {
		t.Fatal("missing criteria did not explain its guarded repair", code, stderr.String())
	}
	versions := map[string]int64{}
	for _, id := range []string{"goal", "work"} {
		var rev int64
		if err = st.DB().QueryRow(`SELECT plan_revision FROM bead_tasks WHERE project_id=? AND id=?`, pid, id).Scan(&rev); err != nil {
			t.Fatal(err)
		}
		versions[id] = rev
	}
	payload, err := json.Marshal(beads.PlanDocument{Dir: dir, Basis: beads.PlanBasis{RequestID: "prepare-existing-leaf", JurisdictionRevision: 1, Beads: versions}, Ops: [][]string{{"update", "work", "--description", "Inspect the existing failure read-only within the agreed goal; do not deploy.", "--acceptance", "Record the failing path, evidence and bounded next step without modifying production."}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "plan.json")
	if err = os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	stderr.Reset()
	if code := runPlan([]string{"apply", "--file", path}, &out, &stderr); code != 0 {
		t.Fatal("ordinary leaf cannot receive executable criteria", code, stderr.String())
	}
	var revision int64
	if err = st.DB().QueryRow(`SELECT plan_revision FROM bead_tasks WHERE project_id=? AND id='work'`, pid).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	args[6] = strconv.FormatInt(revision, 10)
	out.Reset()
	stderr.Reset()
	if code := runExecution(args, &out, &stderr); code != 0 {
		t.Fatal("prepared leaf could not become ready", code, stderr.String())
	}
	if err = st.ConfigureExecutionSlot(ctx, "private-test-slot", []string{"knowledge"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SetOrchestrationControl(ctx, 1, "active", "private scheduler proof"); err != nil {
		t.Fatal(err)
	}
	claim, err := st.ClaimNextExecution(ctx, "private-test-slot", 1, time.Now())
	if err != nil || claim == nil || claim.BeadID != "work" {
		t.Fatal("generic scheduler could not claim prepared work", claim, err)
	}
	var labels string
	if err = st.DB().QueryRow(`SELECT labels::text FROM bead_tasks WHERE project_id=? AND id='held'`, pid).Scan(&labels); err != nil || labels != `["hold"]` {
		t.Fatal("planning repair altered human direction", labels, err)
	}
}
