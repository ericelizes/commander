package daemon

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example-user/commander/internal/api"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
)

func TestPostgresAutomaticGateWatcherUsesCanonicalServiceAndCurrentCommander(t *testing.T) {
	st, dsn := pgtest.Open(t)
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_ROLE", "")
	if e := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	if e := os.Mkdir(filepath.Join(dir, ".beads"), 0700); e != nil {
		t.Fatal(e)
	}
	p := store.Project{Name: "live-auto", Path: dir, Type: "knowledge", Status: "active"}
	pid, e := st.InsertProject(ctx, p)
	if e != nil {
		t.Fatal(e)
	}
	p.ID = pid
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,labels,created_at,updated_at) VALUES(?,'stage','Release','["kind:objective"]','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,parent_id,title,description,labels,created_at,updated_at) VALUES(?,'auto','stage','Tests passed','fixture-check','["kind:gate","gate-of:stage","gate-name:tests","verifier:auto"]','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	if e = st.RegisterCommander(ctx, "commander-urgent", "Urgent"); e != nil {
		t.Fatal(e)
	}
	if _, e = st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: "stage", ExpectedOwner: "commander", ExpectedRevision: 1, NextCommander: "commander-urgent", Reason: "urgent release"}); e != nil {
		t.Fatal(e)
	}
	if _, e = st.SetOrchestrationControl(ctx, 1, "active", "automatic watcher test"); e != nil {
		t.Fatal(e)
	}
	var runs, legacy atomic.Int32
	var fail atomic.Bool
	server := httptest.NewServer(api.New(api.Options{Token: "live-auto", StateStore: st, AutomaticGateRun: func(_ context.Context, actualDir, command string) (string, error) {
		runs.Add(1)
		if actualDir != dir || command != "fixture-check" {
			return "", errors.New("not the stored check")
		}
		if fail.Load() {
			return "actual failure", errors.New("failed")
		}
		return "actual success", nil
	}}).Handler())
	defer server.Close()
	for name, value := range map[string]string{"api-url": server.URL, "api-token": "live-auto"} {
		if e = os.WriteFile(filepath.Join(home, name), []byte(value), 0600); e != nil {
			t.Fatal(e)
		}
	}
	now := time.Now()
	var roles []string
	watcher := gateWatcher{projects: st, beads: beads.NewCached(beads.New()), run: func(context.Context, string, string) (string, error) {
		legacy.Add(1)
		return "", errors.New("local watcher must not run shell")
	}, repoOf: func(string) string { return "" }, now: func() time.Time { return now }, degraded: func() bool { return true }, last: map[string]gateSeen{}, failing: map[string]int{}, mail: func(_ context.Context, to, subject, body string) error { roles = append(roles, to); return nil }}
	// A display-cache failure cannot suspend canonical database verification.
	watcher.poll(ctx)
	var status string
	if e = st.DB().QueryRowContext(ctx, `SELECT status FROM bead_tasks WHERE project_id=? AND id='auto'`, pid).Scan(&status); e != nil || status != "closed" || runs.Load() != 1 || legacy.Load() != 0 || len(roles) != 1 || roles[0] != "commander-urgent" {
		t.Fatalf("canonical watcher did not advance or routed to a legacy Manager: status=%s runs=%d legacy=%d roles=%v err=%v", status, runs.Load(), legacy.Load(), roles, e)
	}
	fail.Store(true)
	now = now.Add(4 * time.Minute)
	watcher.poll(ctx)
	if e = st.DB().QueryRowContext(ctx, `SELECT status FROM bead_tasks WHERE project_id=? AND id='auto'`, pid).Scan(&status); e != nil || status != "open" || runs.Load() != 2 || legacy.Load() != 0 || len(roles) != 2 || roles[1] != "commander-urgent" {
		t.Fatalf("failed condition was kept green: %s runs=%d legacy=%d roles=%v %v", status, runs.Load(), legacy.Load(), roles, e)
	}
	var progress int64
	if e = st.DB().QueryRowContext(ctx, `SELECT progress_seq FROM bead_tasks WHERE project_id=? AND id='auto'`, pid).Scan(&progress); e != nil {
		t.Fatal(e)
	}
	now = now.Add(4 * time.Minute)
	watcher.poll(ctx)
	var again int64
	if e = st.DB().QueryRowContext(ctx, `SELECT progress_seq FROM bead_tasks WHERE project_id=? AND id='auto'`, pid).Scan(&again); e != nil || again != progress || runs.Load() != 3 || len(roles) != 2 {
		t.Fatal("unchanged failed observation fabricated progress or another handoff", progress, again, runs.Load(), roles, e)
	}
	// Optional mail is not authority over condition checking or publication.
	watcher.mail = nil
	fail.Store(false)
	now = now.Add(4 * time.Minute)
	watcher.poll(ctx)
	if e = st.DB().QueryRowContext(ctx, `SELECT status FROM bead_tasks WHERE project_id=? AND id='auto'`, pid).Scan(&status); e != nil || status != "closed" || runs.Load() != 4 {
		t.Fatal("mail outage disabled canonical checking", status, runs.Load(), e)
	}

	// Automatic recovery must not run commands through a recorded pause or an
	// unapproved predecessor, regardless of the project's execution capability.
	if _, e = st.DB().ExecContext(ctx, `UPDATE bead_tasks SET labels='["kind:objective","hold"]' WHERE project_id=? AND id='stage'`, pid); e != nil {
		t.Fatal(e)
	}
	now = now.Add(4 * time.Minute)
	watcher.poll(ctx)
	if runs.Load() != 4 {
		t.Fatal("periodic check ignored the recorded hold", runs.Load())
	}
	if _, e = st.DB().ExecContext(ctx, `UPDATE bead_tasks SET labels='["kind:objective"]' WHERE project_id=? AND id='stage'`, pid); e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,labels,created_at,updated_at) VALUES(?,'approval','Explicit approval','["kind:gate","verifier:user"]','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_dependencies(project_id,issue_id,depends_on_id,type) VALUES(?,'stage','approval','blocks')`, pid); e != nil {
		t.Fatal(e)
	}
	now = now.Add(4 * time.Minute)
	watcher.poll(ctx)
	if runs.Load() != 4 {
		t.Fatal("periodic check bypassed its unapproved prerequisite", runs.Load())
	}

}
