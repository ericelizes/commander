package store

import (
	"fmt"
	"testing"
	"time"
)

// Active offers retain the original scheduler slot; accepting cannot borrow
// a replacement reservation or outlive a revision/epoch or mode change.
func TestPostgresPlaceAcceptanceBoundClaim(t *testing.T) {
	for _, change := range []string{"unchanged", "disabled", "revision", "epoch", "shadow", "slot-removed"} {
		t.Run(change, func(t *testing.T) {
			s, pid, ctx := executionFixture(t)
			c, e := s.ClaimNextExecution(ctx, "vm-0", 2, time.Now())
			if e != nil || c == nil {
				t.Fatal(c, e)
			}
			if e = s.AddPlace(ctx, Place{Name: "mac:claimed", Kind: "mac", PublicKey: "key", PairedAt: 1, PairedBy: "user"}); e != nil {
				t.Fatal(e)
			}
			id, e := s.InsertPlaceOffer(ctx, AgentRun{ProjectID: pid, TaskBeadID: c.BeadID, AgentName: "operator-claimed", ExecutionClaimToken: c.Token, SpawnTime: time.Now().Unix()}, PlaceRun{Place: "mac:claimed", StepID: c.BeadID, OfferedAt: time.Now().Unix()})
			if e != nil {
				t.Fatal(e)
			}
			switch change {
			case "disabled":
				_, e = s.DB().Exec(`UPDATE execution_slots SET enabled=FALSE WHERE run_id=?`, id)
			case "revision":
				_, e = s.DB().Exec(`UPDATE bead_tasks SET plan_revision=plan_revision+1 WHERE project_id=? AND id=?`, pid, c.BeadID)
			case "epoch":
				_, e = s.DB().Exec(`UPDATE bead_tasks SET responsibility_epoch=responsibility_epoch+1 WHERE project_id=? AND id=?`, pid, c.BeadID)
			case "shadow":
				_, e = s.DB().Exec(`UPDATE orchestration_control SET mode='shadow'`)
			case "slot-removed":
				_, e = s.DB().Exec(`UPDATE execution_slots SET run_id=NULL WHERE run_id=?`, id)
			}
			if e != nil {
				t.Fatal(e)
			}
			minted := false
			_, ok, e := s.AcceptPlaceRunWithToken(ctx, id, time.Now().Unix(), func() (string, string) { minted = true; return "token", "token-hash" })
			if e != nil {
				t.Fatal(e)
			}
			if minted != (change == "unchanged") {
				t.Fatalf("token mint invoked=%v change=%s", minted, change)
			}
			if ok != (change == "unchanged") {
				t.Fatalf("accepted=%v change=%s", ok, change)
			}
			pr, e := s.GetPlaceRun(ctx, id)
			if e != nil {
				t.Fatal(e)
			}
			if change != "unchanged" && pr.TokenHash != "" {
				t.Fatal("refusal persisted a token")
			}
		})
	}
}

func TestPostgresPlaceAcceptanceConcurrentHold(t *testing.T) {
	s, pid, ctx := executionFixture(t)
	c, e := s.ClaimNextExecution(ctx, "vm-0", 2, time.Now())
	if e != nil || c == nil {
		t.Fatal(c, e)
	}
	if e = s.AddPlace(ctx, Place{Name: "mac:race", Kind: "mac", PublicKey: "key", PairedAt: 1, PairedBy: "user"}); e != nil {
		t.Fatal(e)
	}
	id, e := s.InsertPlaceOffer(ctx, AgentRun{ProjectID: pid, TaskBeadID: c.BeadID, AgentName: "operator-claimed", ExecutionClaimToken: c.Token}, PlaceRun{Place: "mac:race", StepID: c.BeadID})
	if e != nil {
		t.Fatal(e)
	}
	tx, e := s.DB().BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(734031823)`); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		minted := false
		token, ok, err := s.AcceptPlaceRunWithToken(ctx, id, time.Now().Unix(), func() (string, string) { minted = true; return "token", "hash" })
		if err == nil && (minted || ok || token != "") {
			err = fmt.Errorf("concurrent hold admitted offer: minted=%v accepted=%v", minted, ok)
		}
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if e = s.DB().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND objid=734031823 AND NOT granted)`).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("acceptance did not reach graph fence")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, e = tx.ExecContext(ctx, `UPDATE bead_tasks SET labels='["hold"]' WHERE project_id=? AND id=?`, pid, c.BeadID); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	select {
	case e = <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("acceptance did not return")
	}
	pr, e := s.GetPlaceRun(ctx, id)
	if e != nil || pr.TokenHash != "" || pr.State != PlaceOffered {
		t.Fatalf("offer=%+v err=%v", pr, e)
	}
}
