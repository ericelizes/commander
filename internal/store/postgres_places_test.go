package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// Uses only the disposable trial database and a test-owned schema.
func TestPostgresPlaceClaimAndPresence(t *testing.T) {
	s := postgresTestStore(t)
	ctx := context.Background()
	pid, err := s.InsertProject(ctx, Project{Name: "trial", Type: "code", Path: t.TempDir(), Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"trial-1", "trial-2"} {
		if _, err = s.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) VALUES(?,?,'Task','now','now')`, pid, id); err != nil {
			t.Fatal(err)
		}
	}
	p := Place{Name: "mac:trial", Kind: "mac", PublicKey: "key", PairedAt: 1, PairedBy: "user"}
	if err = s.AddPlace(ctx, p); err != nil {
		t.Fatal(err)
	}
	offer := func(task string) (int64, error) {
		return s.InsertPlaceOffer(ctx, AgentRun{ProjectID: pid, TaskBeadID: task, AgentName: "operator-" + task, SpawnTime: time.Now().Unix()}, PlaceRun{Place: p.Name, StepID: "trial-step", ManagerRole: "manager-trial", BriefSHA: "brief", OfferedAt: time.Now().Unix()})
	}
	var wg sync.WaitGroup
	ids := make(chan int64, 2)
	errs := make(chan error, 2)
	for _, task := range []string{"trial-1", "trial-2"} {
		wg.Add(1)
		go func(task string) { defer wg.Done(); id, e := offer(task); ids <- id; errs <- e }(task)
	}
	wg.Wait()
	close(ids)
	close(errs)
	var id int64
	for v := range ids {
		if v > 0 {
			id = v
		}
	}
	busy := 0
	for e := range errs {
		if errors.Is(e, ErrPlaceBusy) {
			busy++
		} else if e != nil {
			t.Fatal(e)
		}
	}
	if busy != 1 || id == 0 {
		t.Fatalf("id=%d busy=%d", id, busy)
	}
	run, e := s.GetAgentRun(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	var claim int64
	if e = s.DB().QueryRow(`SELECT active_run_id FROM bead_tasks WHERE project_id=? AND id=?`, pid, run.TaskBeadID).Scan(&claim); e != nil || claim != id {
		t.Fatalf("claim %d %v", claim, e)
	}
	if _, e = s.InsertAgentRun(ctx, AgentRun{ProjectID: pid, TaskBeadID: run.TaskBeadID, AgentName: "operator-other"}); e == nil {
		t.Fatal("place claim stolen by host")
	}
	alive, _, known, e := s.RuntimePresence(ctx)
	if e != nil || !known || len(alive) != 0 {
		t.Fatalf("offered presence %v %v %v", alive, known, e)
	}
	if ok, e := s.AcceptPlaceRun(ctx, id, "token", time.Now().Unix()); e != nil || !ok {
		t.Fatalf("accept %v %v", ok, e)
	}
	// The local sampler cannot replace remote heartbeat evidence with absent-local-process.
	if e = s.RecordRuntimePresence(ctx, map[int64]bool{id: false}, time.Now(), nil); e != nil {
		t.Fatal(e)
	}
	alive, _, known, e = s.RuntimePresence(ctx)
	if e != nil || !known || len(alive) != 1 {
		t.Fatalf("heartbeat presence %v %v %v", alive, known, e)
	}
	if _, e = s.BeatPlaceRun(ctx, id, time.Now().Add(-3*time.Minute).Unix()); e != nil {
		t.Fatal(e)
	}
	alive, _, known, e = s.RuntimePresence(ctx)
	if e != nil || known || len(alive) != 0 {
		t.Fatalf("stale presence %v %v %v", alive, known, e)
	}
	for i := 0; i < 2; i++ {
		ok, e := s.RecordPlaceComment(ctx, id, "comment", 1)
		if e != nil || ok != (i == 0) {
			t.Fatalf("dedup %v %v", ok, e)
		}
	}
	if e = s.MarkPlaceRun(ctx, id, "reported", true); e != nil {
		t.Fatal(e)
	}
	if ok, e := s.EndPlaceRun(ctx, id, PlaceStopped, "user", time.Now().Unix()); e != nil || !ok {
		t.Fatalf("end %v %v", ok, e)
	}
	if _, e = offer(run.TaskBeadID); e != nil {
		t.Fatalf("replacement: %v", e)
	}
}

func TestPostgresPlaceOfferPreservesActiveAdmission(t *testing.T) {
	s := postgresTestStore(t)
	ctx := context.Background()
	pid, err := s.InsertProject(ctx, Project{Name: "active-place", Type: "code", Path: t.TempDir(), Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) VALUES(?,'place-active','Task','now','now')`, pid); err != nil {
		t.Fatal(err)
	}
	if err = s.AddPlace(ctx, Place{Name: "mac:active", Kind: "mac", PublicKey: "key", PairedBy: "user", PairedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB().Exec(`UPDATE orchestration_control SET mode='active'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.InsertPlaceOffer(ctx, AgentRun{ProjectID: pid, TaskBeadID: "place-active", AgentName: "operator-active"}, PlaceRun{Place: "mac:active", StepID: "place-active", ManagerRole: "commander", BriefSHA: "brief", OfferedAt: 1}); err == nil {
		t.Fatal("remote offer bypassed canonical admission")
	}
	var runs, offers int
	if err = s.DB().QueryRow(`SELECT count(*) FROM agent_runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err = s.DB().QueryRow(`SELECT count(*) FROM place_runs`).Scan(&offers); err != nil {
		t.Fatal(err)
	}
	if runs != 0 || offers != 0 {
		t.Fatal("failed admission left partial claim or offer", runs, offers)
	}
}
