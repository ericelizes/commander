package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/pgbind"
)

// Explicitly opt-in: this mutates only a restored rehearsal database on the
// separate trial server. The production selector and standard test DB refuse.
func TestRestoredProductionCutoverPreservesDurableWork(t *testing.T) {
	dsn := os.Getenv("COMMANDER_MIGRATION_REHEARSAL_DSN")
	if dsn == "" {
		t.Skip("requires an explicitly restored private production copy")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() != "127.0.0.1" || u.Port() != "55432" || !strings.HasPrefix(u.Path, "/commander_rehearsal_") || u.Query().Get("search_path") != "" {
		t.Fatal("rehearsal requires a dedicated restored database on the trial server")
	}
	ctx := context.Background()
	db, err := sql.Open(pgbind.Driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err = db.QueryRow("SELECT version FROM commander_schema WHERE id=1").Scan(&version); err != nil || (version != 1 && version != 2) {
		t.Fatal("rehearsal must begin from installed schema 1 or 2", version, err)
	}
	tables := []string{"projects", "bead_tasks", "bead_dependencies", "bead_comments", "bead_ranks", "workstream_ranks", "workstream_managers", "pitches", "needs_resolutions", "gates", "agent_runs", "usage_turns", "chat_costs", "agent_sessions"}
	if version >= 2 {
		tables = append(tables, "bead_questions")
	}
	columns := map[string][]string{}
	for _, table := range tables {
		rows, e := db.Query("SELECT column_name FROM information_schema.columns WHERE table_schema='public' AND table_name=? ORDER BY ordinal_position", table)
		if e != nil {
			t.Fatal(e)
		}
		for rows.Next() {
			var name string
			if e = rows.Scan(&name); e != nil {
				t.Fatal(e)
			}
			if name != "state_revision" {
				columns[table] = append(columns[table], name)
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			t.Fatal(e)
		}
		if len(columns[table]) == 0 {
			t.Fatal("missing durable source table", table)
		}
	}
	digest := func(ownershipMayChange bool) map[string]string {
		t.Helper()
		result := map[string]string{}
		for _, table := range tables {
			quoted := []string{}
			for _, col := range columns[table] {
				if ownershipMayChange && table == "bead_tasks" && (col == "responsible_agent_id" || col == "responsibility_epoch") {
					continue
				}
				quoted = append(quoted, `"`+strings.ReplaceAll(col, `"`, `""`)+`"`)
			}
			query := "SELECT COALESCE(jsonb_agg(v ORDER BY v::text),'[]'::jsonb)::text FROM (SELECT to_jsonb(t) AS v FROM (SELECT " + strings.Join(quoted, ",") + " FROM " + table + ") t) x"
			var text string
			if e := db.QueryRow(query).Scan(&text); e != nil {
				t.Fatal(table, e)
			}
			result[table] = fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
		}
		return result
	}
	before := digest(false)
	t.Setenv("COMMANDER_HOME", t.TempDir())
	migrationStarted := time.Now()
	st, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatal("migration failed", err)
	}
	defer st.Close()
	t.Logf("Populated schema migration completed in %s", time.Since(migrationStarted))
	if after := digest(false); !reflect.DeepEqual(before, after) {
		t.Fatal("schema upgrade changed existing durable facts")
	}
	before = digest(true)
	projects, err := st.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	removed := 0
	for _, project := range projects {
		inv, e := st.LegacyPlanningInventory(ctx, project.ID)
		if e != nil {
			t.Fatal(e)
		}
		if len(inv.Boundaries) == 0 {
			continue
		}
		req := JurisdictionConsolidation{ProjectID: project.ID, Revision: inv.Revision, DefaultCommander: inv.DefaultCommander, ControlRevision: inv.ControlRevision, Snapshot: inv.Snapshot, RequestID: "restored-cutover-rehearsal", Reason: "Verify preservation on private restored state"}
		result, e := st.ConsolidateLegacyPlanning(ctx, "", req)
		if e != nil {
			t.Fatal(project.Name, e)
		}
		removed += len(result.Removed)
		after, e := st.LegacyPlanningInventory(ctx, project.ID)
		if e != nil || len(after.Boundaries) != 0 {
			t.Fatal("imported planning boundaries remain", after, e)
		}
	}
	if after := digest(true); !reflect.DeepEqual(before, after) {
		for table, hash := range before {
			if after[table] != hash {
				t.Errorf("durable source changed: %s before=%s after=%s", table, hash, after[table])
			}
		}
	}
	control, err := st.OrchestrationControl(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.SetOrchestrationControl(ctx, control.Revision, "active", "private restored-state rehearsal"); err != nil {
		t.Fatal(err)
	}
	if after := digest(true); !reflect.DeepEqual(before, after) {
		t.Fatal("activation changed durable work")
	}
	var beads, runs int
	if err = db.QueryRow("SELECT count(*) FROM bead_tasks WHERE deleted_at IS NULL").Scan(&beads); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT count(*) FROM agent_runs WHERE status='running'").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	t.Logf("Restored schema %d -> %d; projects=%d beads=%d imported boundaries consolidated=%d running executions preserved=%d; %d durable table fingerprints unchanged. No model runtime started.", version, PostgresSchemaVersion, len(projects), beads, removed, runs, len(tables))
}
