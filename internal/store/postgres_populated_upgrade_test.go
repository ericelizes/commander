package store

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestPostgresPopulatedSchemaThreeToFive(t *testing.T) {
	s, _ := composedFixture(t, 3)
	for _, statement := range []string{
		`INSERT INTO projects(id,name,path,type,status,activated_at) VALUES(42,'existing','/existing','code','active',1)`,
		`INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) VALUES(42,'root','Goal','now','now'),(42,'child','Work','now','now')`,
		`INSERT INTO bead_dependencies(project_id,issue_id,depends_on_id,type) VALUES(42,'child','root','blocks')`,
		`INSERT INTO agent_runs(id,project_id,task_bead_id,agent_name,opencode_session_id,spawn_time,status) VALUES(17,42,'child','operator-existing','retained-session',1,'running')`,
		`UPDATE bead_tasks SET active_run_id=17,progress_seq=73,responsibility_epoch=19 WHERE id='child'`,
	} {
		if _, err := s.DB().Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	var beforeRun, beforeProgress, beforeEpoch int
	if err := s.DB().QueryRow(`SELECT active_run_id,progress_seq,responsibility_epoch FROM bead_tasks WHERE id='child'`).Scan(&beforeRun, &beforeProgress, &beforeEpoch); err != nil {
		t.Fatal(err)
	}
	opened, err := OpenPostgres(s.postgresDSN)
	if err != nil {
		t.Fatal("populated schema 3 upgrade:", err)
	}
	defer opened.Close()
	var version, target, run, progress, epoch int
	if err = opened.DB().QueryRow(`SELECT version FROM commander_schema`).Scan(&version); err != nil || version != 5 {
		t.Fatal(version, err)
	}
	if err = opened.DB().QueryRow(`SELECT target_project_id FROM bead_dependencies WHERE issue_id='child'`).Scan(&target); err != nil || target != 42 {
		t.Fatal("dependency backfill", target, err)
	}
	if err = opened.DB().QueryRow(`SELECT active_run_id,progress_seq,responsibility_epoch FROM bead_tasks WHERE id='child'`).Scan(&run, &progress, &epoch); err != nil || run != beforeRun || progress != beforeProgress || epoch != beforeEpoch {
		t.Fatal("custody/progress changed", run, progress, epoch, err)
	}
	if err = opened.migratePostgres(context.Background()); err != nil {
		t.Fatal("reopen", err)
	}
}

func TestPostgresManagedTableAdditionsRefused(t *testing.T) {
	var scenario struct {
		ManagedAdditions map[string]string `json:"managed_additions"`
	}
	raw, err := os.ReadFile("../../tests/evals/scenarios/postgres-migration/populated-upgrade.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	if len(scenario.ManagedAdditions) != 3 {
		t.Fatal("missing managed-shape scenarios")
	}
	for name, ddl := range scenario.ManagedAdditions {
		t.Run(name, func(t *testing.T) {
			s := postgresTestStore(t)
			if _, err := s.DB().Exec(ddl); err != nil {
				t.Fatal(err)
			}
			opened, err := OpenPostgres(s.postgresDSN)
			if err == nil {
				opened.Close()
				t.Fatal("managed addition accepted")
			}
			if !strings.Contains(err.Error(), "shape mismatch at unexpected") {
				t.Fatal(err)
			}
		})
	}
}

func TestPostgresUnrelatedObjectsAllowed(t *testing.T) {
	s := postgresTestStore(t)
	if _, err := s.DB().Exec(`CREATE TABLE unrelated_extra(id bigint PRIMARY KEY, x text CHECK(x <> '')); CREATE TRIGGER extra_trigger AFTER INSERT ON unrelated_extra FOR EACH ROW EXECUTE FUNCTION commander_state_event()`); err != nil {
		t.Fatal(err)
	}
	opened, err := OpenPostgres(s.postgresDSN)
	if err != nil {
		t.Fatal("unrelated objects refused", err)
	}
	opened.Close()
}
