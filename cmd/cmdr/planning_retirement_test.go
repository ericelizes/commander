package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/example-user/commander/internal/api"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
)

func TestPlanningRetirementCLI(t *testing.T) {
	st, dsn := pgtest.Open(t)
	ctx := context.Background()
	home, dir := t.TempDir(), t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_ROLE", "commander")
	t.Setenv("COMMANDER_MANAGER_ROLE", "")
	if e := os.Mkdir(filepath.Join(dir, ".beads"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
		t.Fatal(e)
	}
	pid, e := st.InsertProject(ctx, store.Project{Name: "retire-cli", Path: dir, Type: "code", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,status,labels,created_at,updated_at) VALUES(?,'stage','Local delivery','deferred','["kind:objective"]','now','now'),(?,'auto','Merged','open','["kind:gate","gate-of:stage","gate-name:merged","verifier:auto","gate-status:failed"]','now','now'),(?,'block','Obsolete review','open','["kind:objective","workflow-template:pr-review","workflow-instance:old","workflow-hash:saved"]','now','now')`, pid, pid, pid); e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(api.New(api.Options{Token: "retirement-test", StateStore: st}).Handler())
	defer server.Close()
	for name, value := range map[string]string{"api-url": server.URL, "api-token": "retirement-test"} {
		if e = os.WriteFile(filepath.Join(home, name), []byte(value), 0600); e != nil {
			t.Fatal(e)
		}
	}
	oldResolve := resolveProjectRecFn
	resolveProjectRecFn = func(string) (*store.Project, error) { return st.GetProjectByName(ctx, "retire-cli") }
	defer func() { resolveProjectRecFn = oldResolve }()
	if e = gateRetireLegacy(ctx, st, "retire-cli", "stage", "", "superseded local-only delivery", true); e != nil {
		t.Fatal(e)
	}
	var status string
	if e = st.DB().QueryRowContext(ctx, `SELECT status FROM bead_tasks WHERE project_id=? AND id='stage'`, pid).Scan(&status); e != nil || status != "deferred" {
		t.Fatal("dry run wrote status", e)
	}
	if e = gateRetireLegacy(ctx, st, "retire-cli", "stage", "", "superseded local-only delivery", false); e != nil {
		t.Fatal(e)
	}
	out, errOut, read := withTempOutputFiles(t)
	if code := runWorkflow([]string{"retire-block", "--project", "retire-cli", "--block", "block", "--reason", "withdrawn obsolete PR"}, out, errOut); code != 0 {
		_, msg := read()
		t.Fatal(msg)
	}
	for _, id := range []string{"stage", "auto", "block"} {
		if e = st.DB().QueryRowContext(ctx, `SELECT status FROM bead_tasks WHERE project_id=? AND id=?`, pid, id).Scan(&status); e != nil || status != "closed" {
			t.Fatalf("%s: %s %v", id, status, e)
		}
	}
}
