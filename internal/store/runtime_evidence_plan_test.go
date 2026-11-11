package store

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestObservedExecutionQueryPlan(t *testing.T) {
	if os.Getenv("COMMANDER_POSTGRES_QUERY_PLAN") != "1" {
		t.Skip("explicit private query diagnostic")
	}
	s := postgresTestStore(t)
	ctx := context.Background()
	pid, e := s.InsertProject(ctx, Project{Name: "query-plan", Path: t.TempDir(), Type: "code", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	for _, q := range []string{
		`INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) VALUES(?,'root','Root','now','now')`,
		`INSERT INTO bead_tasks(project_id,id,title,parent_id,created_at,updated_at) SELECT ?,'task-'||i,'Task '||i,'root','now','now' FROM generate_series(1,1000)i`,
		`INSERT INTO agent_runs(project_id,task_bead_id,agent_name,opencode_session_id,status,spawn_time,execution_driver) SELECT project_id,id,'executor-'||id,'session-'||id,'running',1,'native' FROM bead_tasks WHERE project_id=? AND id<>'root'`,
		`UPDATE bead_tasks b SET active_run_id=r.id FROM agent_runs r WHERE b.project_id=r.project_id AND b.id=r.task_bead_id AND b.project_id=?`,
		`INSERT INTO agent_state(role,state,detail,since,updated) SELECT agent_name,'working','',1,1 FROM agent_runs WHERE project_id=?`,
	} {
		if _, e = s.DB().ExecContext(ctx, q, pid); e != nil {
			t.Fatal(e)
		}
	}
	rows, e := s.DB().QueryContext(ctx, "EXPLAIN (ANALYZE,BUFFERS) "+observedExecutionSQL, time.Now().Add(-45*time.Second).Unix())
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	for rows.Next() {
		var line string
		if e = rows.Scan(&line); e != nil {
			t.Fatal(e)
		}
		t.Log(line)
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
}
