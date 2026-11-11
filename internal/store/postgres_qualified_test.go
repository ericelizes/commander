package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/pgbind"
)

// Construct historical shapes directly, never by lowering a migrated marker.
func composedFixture(t *testing.T, version int) (*Store, string) {
	t.Helper()
	s := postgresTestStore(t)
	u, err := url.Parse(s.postgresDSN)
	if err != nil {
		t.Fatal(err)
	}
	schema := u.Query().Get("search_path") + "_upgrade"
	if _, err = s.DB().Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB().Exec(`DROP SCHEMA ` + schema + ` CASCADE`) })
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open(pgbind.Driver, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	raw := &Store{db: db, postgres: true, postgresDSN: u.String()}
	if version > 0 {
		if _, err = db.Exec(postgresSchema); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`CREATE TABLE commander_schema(id BIGINT PRIMARY KEY CHECK(id=1),version BIGINT NOT NULL);INSERT INTO commander_schema VALUES(1,1)`); err != nil {
			t.Fatal(err)
		}
	}
	for next, sql := range []string{jurisdictionSchema + beadQuestionsSchema + executionSchema + supervisionSchema, runtimeIntegritySchema, postgresPlacesSchema, postgresQualifiedSchema} {
		n := next + 2
		if version >= n {
			if _, err = db.Exec(sql); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(fmt.Sprintf("UPDATE commander_schema SET version=%d", n)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return raw, schema
}

// Normalize only schema qualification; compare actual columns, constraints,
// indexes, triggers and function bodies for all three independently built paths.
func composedShape(t *testing.T, s *Store, schema string) string {
	t.Helper()
	rows, e := s.DB().Query(`SELECT item FROM (
 SELECT 'column:'||table_name||':'||column_name||':'||data_type||':'||is_nullable||':'||COALESCE(column_default,'') item FROM information_schema.columns WHERE table_schema=current_schema()
 UNION ALL SELECT 'constraint:'||c.relname||':'||pg_get_constraintdef(x.oid) FROM pg_constraint x JOIN pg_class c ON c.oid=x.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema()
 UNION ALL SELECT 'index:'||indexname||':'||indexdef FROM pg_indexes WHERE schemaname=current_schema()
 UNION ALL SELECT 'trigger:'||tgname||':'||pg_get_triggerdef(t.oid) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND NOT t.tgisinternal
 UNION ALL SELECT 'function:'||p.proname||':'||pg_get_functiondef(p.oid) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=current_schema()
 ) x ORDER BY item`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if e = rows.Scan(&v); e != nil {
			t.Fatal(e)
		}
		out = append(out, strings.ReplaceAll(v, schema, "fixture"))
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	return strings.Join(out, "\n")
}

func TestPostgresComposedConvergence(t *testing.T) {
	var expected string
	raw, e := os.ReadFile("../../tests/evals/scenarios/postgres-composed-schema.json")
	if e != nil {
		t.Fatal(e)
	}
	var scenario struct {
		Versions []int `json:"versions"`
	}
	if e = json.Unmarshal(raw, &scenario); e != nil {
		t.Fatal(e)
	}
	if len(scenario.Versions) != 6 {
		t.Fatal("missing migration paths")
	}
	for _, version := range scenario.Versions {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			s, schema := composedFixture(t, version)
			ctx := context.Background()
			var pid int64
			if version > 0 {
				var e error
				pid, e = s.InsertProject(ctx, Project{Name: "fixture", Type: "code", Path: t.TempDir(), Status: "active"})
				if e != nil {
					t.Fatal(e)
				}
				if _, e = s.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) VALUES(?,'fixture-1','Source','now','now'),(?,'fixture-2','Target','now','now')`, pid, pid); e != nil {
					t.Fatal(e)
				}
				if _, e = s.DB().Exec(`INSERT INTO bead_dependencies(project_id,issue_id,depends_on_id,type) VALUES(?,'fixture-1','fixture-2','blocks')`, pid); e != nil {
					t.Fatal(e)
				}
			}
			if version == 4 {
				if e := s.AddPlace(ctx, Place{Name: "mac:fixture", Kind: "mac", PublicKey: "fixture-key", PairedAt: 1, PairedBy: "user"}); e != nil {
					t.Fatal(e)
				}
				// An active offer and canonical claim must survive the actual 2->3 opener.
				id, e := s.InsertPlaceOffer(ctx, AgentRun{ProjectID: pid, TaskBeadID: "fixture-1", AgentName: "operator-fixture", SpawnTime: 1}, PlaceRun{Place: "mac:fixture", StepID: "fixture-step", ManagerRole: "manager-fixture", BriefSHA: "fixture", OfferedAt: 1})
				if e != nil {
					t.Fatal(e)
				}
				if _, e = s.AcceptPlaceRun(ctx, id, "fixture-hash", time.Now().Unix()); e != nil {
					t.Fatal(e)
				}
				if _, e = s.RecordPlaceComment(ctx, id, "fixture-comment", 1); e != nil {
					t.Fatal(e)
				}
			}
			opened, e := OpenPostgres(s.postgresDSN)
			if e != nil {
				t.Fatal(e)
			}
			opened.Close()
			if e = s.migratePostgres(ctx); e != nil {
				t.Fatal(e)
			}
			var got int
			if e = s.DB().QueryRow(`SELECT version FROM commander_schema`).Scan(&got); e != nil || got != PostgresSchemaVersion {
				t.Fatalf("version=%d %v", got, e)
			}
			if version > 0 {
				var target int64
				if e = s.DB().QueryRow(`SELECT target_project_id FROM bead_dependencies`).Scan(&target); e != nil || target != pid {
					t.Fatalf("backfill %d %v", target, e)
				}
			}
			if version == 4 {
				alive, _, known, e := s.RuntimePresence(ctx)
				if e != nil || !known || len(alive) != 1 {
					t.Fatalf("retained heartbeat %v %v %v", alive, known, e)
				}
				var n int
				if e = s.DB().QueryRow(`SELECT count(*) FROM places p JOIN place_runs r ON r.place=p.name JOIN place_comments c ON c.run_id=r.run_id JOIN bead_tasks b ON b.active_run_id=r.run_id WHERE r.state='running' AND r.token_hash='fixture-hash'`).Scan(&n); e != nil || n != 1 {
					t.Fatalf("retained places/claim/comments %d %v", n, e)
				}
			}
			shape := composedShape(t, s, schema)
			if expected == "" {
				expected = shape
			} else if shape != expected {
				t.Fatalf("schema %d path did not converge\n%s", version, shape)
			}
		})
	}
}

func TestPostgresComposedRollback(t *testing.T) {
	for _, version := range []int{1, 3, 4} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			s, _ := composedFixture(t, version)
			// Failure late in migration 3 must roll back its earlier DDL, events and
			// the entire places migration when starting from 1.
			if _, e := s.DB().Exec(`CREATE TABLE qualified_graph_revision(id BIGINT)`); e != nil {
				t.Fatal(e)
			}
			var before int
			if e := s.DB().QueryRow(`SELECT count(*) FROM state_events`).Scan(&before); e != nil {
				t.Fatal(e)
			}
			if opened, e := OpenPostgres(s.postgresDSN); e == nil {
				opened.Close()
				t.Fatal("expected migration failure")
			}
			var got, events, columns, places int
			if e := s.DB().QueryRow(`SELECT version FROM commander_schema`).Scan(&got); e != nil || got != version {
				t.Fatalf("version %d %v", got, e)
			}
			if e := s.DB().QueryRow(`SELECT count(*) FROM state_events`).Scan(&events); e != nil || events != before {
				t.Fatalf("events %d %v", events, e)
			}
			if e := s.DB().QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='bead_dependencies' AND column_name='target_project_id'`).Scan(&columns); e != nil || columns != 0 {
				t.Fatalf("partial qualified DDL %d %v", columns, e)
			}
			if e := s.DB().QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('places','place_runs','place_comments')`).Scan(&places); e != nil || places != func() int {
				if version >= 4 {
					return 3
				}
				return 0
			}() {
				t.Fatalf("partial places DDL %d %v", places, e)
			}
		})
	}
}

func TestPostgresComposedMigrationLock(t *testing.T) {
	s, _ := composedFixture(t, 3)
	tx, e := s.DB().Begin()
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`SELECT pg_advisory_xact_lock(734031820)`); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if e = s.migratePostgres(ctx); e == nil {
		t.Fatal("upgrade bypassed migration lock")
	}
	if e = tx.Rollback(); e != nil {
		t.Fatal(e)
	}
	if e = s.migratePostgres(context.Background()); e != nil {
		t.Fatal(e)
	}
}

func TestComposedPlacesSQLIdentity(t *testing.T) {
	// SHA256 of exact postgres_places.sql at reviewed 158f0064.
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(postgresPlacesSchema))); got != "1e0928e66551367546b0e0f00973ec8801cb65ea5fd0c758d743251e5ecaaba1" {
		t.Fatalf("reserved places SQL changed: %s", got)
	}
}
