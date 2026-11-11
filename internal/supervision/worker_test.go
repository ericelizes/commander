package supervision

import (
	"context"
	"fmt"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func workerFixture(t *testing.T) (*store.Store, int64) {
	t.Helper()
	s, _ := pgtest.Open(t)
	ctx := context.Background()
	pid, e := s.InsertProject(ctx, store.Project{Name: "supervision-fixture", Type: "knowledge", Path: filepath.Join(t.TempDir(), "project"), Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.SetOrchestrationControl(ctx, 1, "active", "supervision fixture"); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"root", "task"} {
		if _, e = s.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,labels,parent_id,created_at,updated_at) VALUES(?,?,?,'["kind:objective"]',NULLIF(?,''),'now','now')`, pid, id, id, map[string]string{"task": "root"}[id]); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.RegisterCommander(ctx, "commander-root", "Fixture"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: "root", ExpectedOwner: "commander", ExpectedRevision: 1, NextCommander: "commander-root", Reason: "fixture"}); e != nil {
		t.Fatal(e)
	}
	return s, pid
}
func TestSupervisorAcknowledgementDuringDeliverySurvivesRestart(t *testing.T) {
	s, pid := workerFixture(t)
	ctx := context.Background()
	calls := 0
	now := time.Now()
	deliver := func(ctx context.Context, role, prompt string) error {
		calls++
		if role != "commander-root" || !strings.Contains(prompt, "--request") {
			t.Fatalf("wrong delivery: %s", prompt)
		}
		var epoch int64
		var request string
		if e := s.DB().QueryRow(`SELECT epoch,request_id FROM bead_supervision WHERE project_id=? AND bead_id='task'`, pid).Scan(&epoch, &request); e != nil {
			t.Fatal(e)
		}
		return s.AcknowledgeSupervision(ctx, pid, "task", epoch, role, request)
	}
	w := &Worker{Store: s, Mode: "active", Now: func() time.Time { return now }, Deliver: deliver}
	if e := w.Pass(ctx); e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatalf("delivery count=%d", calls)
	}
	var state string
	if e := s.DB().QueryRow(`SELECT state FROM bead_supervision WHERE project_id=? AND bead_id='task'`, pid).Scan(&state); e != nil || state != "acknowledged" {
		t.Fatalf("early ack overwritten: %q %v", state, e)
	}
	if _, e := s.DB().Exec(`UPDATE bead_supervision SET due_at=0`); e != nil {
		t.Fatal(e)
	}
	restarted := &Worker{Store: s, Mode: "active", Now: func() time.Time { return now.Add(30 * time.Second) }, Deliver: deliver}
	if e := restarted.Pass(ctx); e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatalf("restart duplicated acknowledged delivery: %d", calls)
	}
}
func TestSupervisorRetriesAndEscalatesWithoutInventingProgress(t *testing.T) {
	s, pid := workerFixture(t)
	ctx := context.Background()
	now := time.Now()
	roles := []string{}
	w := &Worker{Store: s, Mode: "active", Now: func() time.Time { return now }, Deliver: func(_ context.Context, role, _ string) error {
		roles = append(roles, role)
		return fmt.Errorf("simulated transport failure")
	}}
	for i := 0; i < 5; i++ {
		if _, e := s.DB().Exec(`UPDATE bead_supervision SET due_at=0,lease_until=0`); e != nil {
			t.Fatal(e)
		}
		if _, e := s.DB().Exec(`UPDATE supervision_dispatch SET next_delivery_at=0,lease_until=0`); e != nil {
			t.Fatal(e)
		}
		now = now.Add(time.Minute)
		if e := w.Pass(ctx); e != nil {
			t.Fatal(e)
		}
	}
	if len(roles) < 4 || roles[0] != "commander-root" || roles[3] != "commander" {
		t.Fatalf("no bounded escalation: %v", roles)
	}
	var status string
	if e := s.DB().QueryRow(`SELECT status FROM bead_tasks WHERE project_id=? AND id='task'`, pid).Scan(&status); e != nil || status != "open" {
		t.Fatalf("supervision changed workflow status: %q %v", status, e)
	}
}
func TestSupervisorRespectsAncestorHoldAndShadowMode(t *testing.T) {
	s, pid := workerFixture(t)
	ctx := context.Background()
	calls := 0
	w := &Worker{Store: s, Mode: "shadow", Deliver: func(context.Context, string, string) error { calls++; return nil }}
	if e := w.Pass(ctx); e != nil {
		t.Fatal(e)
	}
	if calls != 0 {
		t.Fatal("shadow mode woke an agent")
	}
	if _, e := s.DB().Exec(`UPDATE bead_tasks SET labels=labels||'"hold"'::jsonb WHERE project_id=? AND id='root'`, pid); e != nil {
		t.Fatal(e)
	}
	if _, e := s.DB().Exec(`UPDATE bead_supervision SET due_at=0`); e != nil {
		t.Fatal(e)
	}
	w.Mode = "active"
	if e := w.Pass(ctx); e != nil {
		t.Fatal(e)
	}
	if calls != 0 {
		t.Fatal("ancestor hold was ignored")
	}
}
