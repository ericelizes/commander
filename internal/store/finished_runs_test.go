package store

import (
	"context"
	"testing"
	"time"
)

// Uses only the disposable trial database and a test-owned schema.
func TestPostgresReportedRuns(t *testing.T) {
	s := postgresTestStore(t)
	ctx := context.Background()
	pid, err := s.InsertProject(ctx, Project{Name: "trial", Type: "code", Path: t.TempDir(), Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	at := func(sec int) string { return now.Add(time.Duration(sec) * time.Second).Format(time.RFC3339Nano) }
	mk := func(bead, status string, bind bool) int64 {
		if _, err := s.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) VALUES(?,?,'Task','now','now')`, pid, bead); err != nil {
			t.Fatal(err)
		}
		var id int64
		if err := s.DB().QueryRow(`INSERT INTO agent_runs(project_id,task_bead_id,agent_name,status,spawn_time,opencode_session_id) VALUES(?,?,?,?,?,'') RETURNING id`, pid, bead, "operator-"+bead, status, now.Unix()).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if bind {
			if _, err := s.DB().Exec(`UPDATE bead_tasks SET active_run_id=? WHERE project_id=? AND id=?`, id, pid, bead); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	say := func(bead string, run int64, id, text string, sec int) {
		if _, err := s.DB().Exec(`INSERT INTO bead_comments(project_id,bead_id,id,author,text,created_at,run_id) VALUES(?,?,?,'a',?,?,?)`, pid, bead, id, text, at(sec), run); err != nil {
			t.Fatal(err)
		}
	}
	rep := mk("t-reported", "running", true)
	say("t-reported", rep, "c1", "working", 0)
	say("t-reported", rep, "c2", "Final report: done", 1)
	later := mk("t-later", "running", true)
	say("t-later", later, "c1", "Final report: done", 0)
	say("t-later", later, "c2", "one more thing", 1)
	closed := mk("t-closed", "exited", true)
	say("t-closed", closed, "c1", "Final report: done", 0)
	unbound := mk("t-unbound", "running", false)
	say("t-unbound", unbound, "c1", "Final report: done", 0)

	got, err := s.ReportedRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].BeadID != "t-reported" || got[0].RunID != rep || got[0].ProjectID != pid || got[0].Agent != "operator-t-reported" || got[0].ReportedAt != now.Add(time.Second).Unix() {
		t.Fatalf("got %+v", got)
	}
}
