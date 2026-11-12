package evals

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tui"
)

// Exercise the CLI workstream placement entry points, reopening the
// PostgreSQL-selected store rather than calling its move directly. The sidebar
// shares the underlying store move.
func TestPostgresWorkstreamReorderScenario(t *testing.T) {
	var sc struct {
		Workstreams []string
		Move        string
		To          int
		Expected    []string
	}
	raw, err := os.ReadFile("scenarios/postgres-workstream-reorder.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	st, dsn := pgtest.Open(t)
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	if err = os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pid, err := st.InsertProject(ctx, store.Project{Name: "reorder", Type: "code", Path: t.TempDir(), Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	var present []store.WorkstreamKey
	for _, id := range sc.Workstreams {
		if _, err = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,labels,created_at,updated_at) VALUES(?,?,?,'["kind:objective","kind:workstream"]','now','now')`, pid, id, id); err != nil {
			t.Fatal(err)
		}
		present = append(present, store.WorkstreamKey{ProjectID: pid, BeadID: id})
	}
	if err = st.SetWorkstreamOrder(ctx, present); err != nil {
		t.Fatal(err)
	}
	if _, err = tui.MoveWorkstreamTo(ctx, "reorder", sc.Move, sc.To); err != nil {
		t.Fatal(err)
	}
	// internal-place shares this entry point; repeating the placement is stable.
	if err = tui.PlaceWorkstreamAt(ctx, "reorder", sc.Move, sc.To); err != nil {
		t.Fatal(err)
	}
	ranks, err := st.ListWorkstreamRanks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range sc.Expected {
		if ranks[store.WorkstreamKey{ProjectID: pid, BeadID: id}] != i+1 {
			t.Fatalf("ranks = %v, want %v", ranks, sc.Expected)
		}
	}
}
