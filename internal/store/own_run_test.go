package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOwnRunClaimFences(t *testing.T) {
	db, e := sql.Open("sqlite", ":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		`CREATE TABLE projects(id INTEGER,name TEXT,path TEXT)`,
		`CREATE TABLE agent_runs(id INTEGER,project_id INTEGER,task_bead_id TEXT,agent_name TEXT,opencode_session_id TEXT,worktree_path TEXT,status TEXT)`,
		`CREATE TABLE bead_tasks(id TEXT,project_id INTEGER,active_run_id INTEGER,deleted_at TEXT,status TEXT)`,
	} {
		if _, e := db.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	root, wt := t.TempDir(), t.TempDir()
	db.Exec(`INSERT INTO projects VALUES(1,'trial',?)`, root)
	db.Exec(`INSERT INTO agent_runs VALUES(7,1,'trial-1','operator-trial.1','session',?,'running')`, wt)
	db.Exec(`INSERT INTO bead_tasks VALUES('trial-1',1,7,NULL,'in_progress')`)
	s := &Store{db: db, postgres: true}
	want := OwnRunClaim{7, "trial-1", "operator-trial-1", "session", "trial", root, wt}
	if got, e := s.OwnRun(context.Background(), want); e != nil || got.Actor != "operator-trial.1" {
		t.Fatalf("%+v %v", got, e)
	}
	for _, field := range []string{"run", "task", "actor", "session", "project", "root", "worktree"} {
		t.Run(field, func(t *testing.T) {
			bad := want
			switch field {
			case "run":
				bad.RunID++
			case "task":
				bad.Task = "trial-2"
			case "actor":
				bad.Actor = "operator-other"
			case "session":
				bad.Session = "other"
			case "project":
				bad.Project = "other"
			case "root":
				bad.ProjectDir = wt
			case "worktree":
				bad.Worktree = root
			}
			if got, e := s.OwnRun(context.Background(), bad); e == nil || got.RunID != 0 {
				t.Fatalf("disclosed mismatch: %+v %v", got, e)
			}
		})
	}
	for _, q := range []string{`UPDATE bead_tasks SET active_run_id=8`, `UPDATE bead_tasks SET active_run_id=7,deleted_at='now'`, `UPDATE bead_tasks SET deleted_at=NULL,status='closed'`, `UPDATE bead_tasks SET status='in_progress'; UPDATE agent_runs SET status='exited'`, `UPDATE agent_runs SET status='running',worktree_path=NULL`} {
		if _, e := db.Exec(q); e != nil {
			t.Fatal(e)
		}
		if _, e := s.OwnRun(context.Background(), want); e == nil {
			t.Fatal("accepted inactive claim")
		}
	}
}

func TestOwnRunNoLegacyFallback(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.db")
	if _, e := OpenOwnRunReader(context.Background(), path); e == nil {
		t.Fatal("opened legacy")
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("created legacy DB")
	}
	os.WriteFile(filepath.Join(root, "state.postgres"), []byte("invalid sensitive configuration"), 0600)
	if _, e := OpenOwnRunReader(context.Background(), path); e == nil {
		t.Fatal("accepted invalid selector")
	}
}

func TestOwnRunPostgresReadOnly(t *testing.T) {
	s := postgresTestStore(t)
	ctx := context.Background()
	root, wt := t.TempDir(), t.TempDir()
	pid, e := s.InsertProject(ctx, Project{Name: "trial", Type: "code", Path: root, Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.Exec(`INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) VALUES(?,'trial-1','Task','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	id, e := s.InsertAgentRun(ctx, AgentRun{ProjectID: pid, TaskBeadID: "trial-1", AgentName: "operator-trial", OpencodeSessionID: "session", WorktreePath: sql.NullString{String: wt, Valid: true}, Status: "running"})
	if e != nil {
		t.Fatal(e)
	}
	selector := t.TempDir()
	os.WriteFile(filepath.Join(selector, "state.postgres"), []byte(s.postgresDSN), 0600)
	ro, e := OpenOwnRunReader(ctx, filepath.Join(selector, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer ro.Close()
	if _, e := ro.OwnRun(ctx, OwnRunClaim{id, "trial-1", "operator-trial", "session", "trial", root, wt}); e != nil {
		t.Fatal(e)
	}
	if _, e := ro.db.Exec(`UPDATE agent_runs SET status='closed' WHERE id=?`, id); e == nil {
		t.Fatal("reader allowed writes")
	}
}

func TestOwnRunBootstrapScenario(t *testing.T) {
	b, e := os.ReadFile("../../tests/evals/scenarios/own-run-bootstrap.json")
	if e != nil {
		t.Fatal(e)
	}
	var scenario struct {
		Cases []string `json:"cases"`
	}
	if e = json.Unmarshal(b, &scenario); e != nil {
		t.Fatal(e)
	}
	if len(scenario.Cases) != 3 {
		t.Fatal("missing cases")
	}
	for _, c := range scenario.Cases {
		switch c {
		case "claim-fences":
			t.Run(c, TestOwnRunClaimFences)
		case "no-legacy":
			t.Run(c, TestOwnRunNoLegacyFallback)
		case "postgres-read-only":
			t.Run(c, TestOwnRunPostgresReadOnly)
		default:
			t.Fatal(c)
		}
	}
}
