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

func TestPostgresJurisdictionConsolidationCLIUsesReviewedInventoryAndCanonicalService(t *testing.T) {
	var scenario struct {
		Key    string `json:"request_id"`
		Reason string `json:"reason"`
	}
	rawScenario, e := os.ReadFile(filepath.Join("..", "..", "tests", "evals", "scenarios", "commander-planning-consolidation.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(rawScenario, &scenario); e != nil || scenario.Key == "" {
		t.Fatal("missing consolidation scenario", e)
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
	pid, e := st.InsertProject(ctx, store.Project{Name: "consolidation-cli", Type: "knowledge", Path: t.TempDir(), Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().Exec(`INSERT INTO bead_tasks(project_id,id,parent_id,title,created_at,updated_at) VALUES(?,'home',NULL,'Home','now','now'),(?,'urgent','home','Urgent','now','now')`, pid, pid); e != nil {
		t.Fatal(e)
	}
	if e = st.UpsertWorkstreamManager(ctx, store.WorkstreamManager{ProjectID: pid, WorkstreamBeadID: "home", AmRole: "manager-imported", SessionFile: "retained-chat"}); e != nil {
		t.Fatal(e)
	}
	if e = st.WithTx(ctx, func(q store.Querier) error { return store.SeedImportedJurisdictions(ctx, q, pid) }); e != nil {
		t.Fatal(e)
	}
	if e = st.RegisterCommander(ctx, "commander-urgent", "Urgent"); e != nil {
		t.Fatal(e)
	}
	if _, e = st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: "urgent", ExpectedOwner: "manager-imported", ExpectedRevision: 1, NextCommander: "commander-urgent", Reason: "explicit urgent thread"}); e != nil {
		t.Fatal(e)
	}
	handler := api.New(api.Options{Token: "consolidation-test", StateStore: st}).Handler()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == api.JurisdictionConsolidationPath {
			calls.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	for name, value := range map[string]string{"api-url": server.URL, "api-token": "consolidation-test"} {
		if e = os.WriteFile(filepath.Join(home, name), []byte(value), 0600); e != nil {
			t.Fatal(e)
		}
	}
	var out, errOut bytes.Buffer
	if code := runJurisdiction([]string{"inventory", "--project-id", strconv.FormatInt(pid, 10)}, &out, &errOut); code != 0 {
		t.Fatal(code, errOut.String())
	}
	var inventory store.LegacyPlanningInventory
	if e = json.Unmarshal(out.Bytes(), &inventory); e != nil || len(inventory.Boundaries) != 1 || inventory.Boundaries[0].CommanderID != "manager-imported" {
		t.Fatal(inventory, e)
	}
	req := store.JurisdictionConsolidation{ProjectID: pid, Revision: inventory.Revision, DefaultCommander: inventory.DefaultCommander, ControlRevision: inventory.ControlRevision, Snapshot: inventory.Snapshot, RequestID: scenario.Key, Reason: scenario.Reason}
	raw, _ := json.Marshal(req)
	path := filepath.Join(home, "consolidation.json")
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("COMMANDER_ROLE", "commander-urgent")
	out.Reset()
	errOut.Reset()
	if code := runJurisdiction([]string{"consolidate", "--file", path}, &out, &errOut); code == 0 {
		t.Fatal("foreign Commander took project planning ownership")
	}
	t.Setenv("COMMANDER_ROLE", "commander")
	t.Setenv("COMMANDER_RUN_SESSION", "execution-session")
	out.Reset()
	errOut.Reset()
	if code := runJurisdiction([]string{"consolidate", "--file", path}, &out, &errOut); code == 0 {
		t.Fatal("execution attribution migrated planning")
	}
	t.Setenv("COMMANDER_RUN_SESSION", "")
	for i := 0; i < 2; i++ {
		out.Reset()
		errOut.Reset()
		if code := runJurisdiction([]string{"consolidate", "--file", path}, &out, &errOut); code != 0 {
			t.Fatal(code, errOut.String())
		}
		var result store.JurisdictionConsolidationResult
		if e = json.Unmarshal(out.Bytes(), &result); e != nil || result.Replayed != (i == 1) || len(result.Removed) != 1 {
			t.Fatal(result, e)
		}
	}
	if calls.Load() != 4 {
		t.Fatal("migration did not delegate once per action", calls.Load())
	}
	for id, owner := range map[string]string{"home": "commander", "urgent": "commander-urgent"} {
		j, e := st.Jurisdiction(ctx, pid, id)
		if e != nil || j.CommanderID != owner {
			t.Fatal(id, j, e)
		}
	}
	server.Close()
	out.Reset()
	errOut.Reset()
	if code := runJurisdiction([]string{"consolidate", "--file", path}, &out, &errOut); code == 0 {
		t.Fatal("missing canonical service used a local mutation fallback")
	}
}
