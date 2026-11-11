package store

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"
)

// Explicit release gate: exercise a thousand complete claim/result/settle
// cycles over a populated queue. No runtime or model is involved.
func TestPostgresExecutionQueueAtScale(t *testing.T) {
	if os.Getenv("COMMANDER_EXECUTION_LOAD") != "1" {
		t.Skip("explicit 1000-assignment release gate")
	}
	st := postgresTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	pid, e := st.InsertProject(ctx, Project{Name: "execution-load", Path: t.TempDir(), Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.SetOrchestrationControl(ctx, 1, "active", "isolated execution load fixture"); e != nil {
		t.Fatal(e)
	}
	if e = st.ConfigureExecutionSlot(ctx, "load-slot", []string{"knowledge"}, true); e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,description,acceptance_criteria,labels,execution_ready,created_at,updated_at) SELECT ?, 'load-'||i,'Task '||i,'Implement bounded fixture','Evidence passes','["kind:task"]',TRUE,'now','now' FROM generate_series(1,1000) i`, pid); e != nil {
		t.Fatal(e)
	}
	durations := make([]time.Duration, 0, 1000)
	seen := map[string]bool{}
	started := time.Now()
	for i := 0; i < 1000; i++ {
		at := time.Now()
		claim, e := st.ClaimNextExecution(ctx, "load-slot", 1, at)
		durations = append(durations, time.Since(at))
		if e != nil || claim == nil {
			t.Fatalf("claim %d missing: %v", i, e)
		}
		if seen[claim.BeadID] {
			t.Fatalf("duplicate assignment %s", claim.BeadID)
		}
		seen[claim.BeadID] = true
		session := fmt.Sprintf("load-session-%d", i)
		rid, e := st.InsertAgentRun(ctx, AgentRun{ExecutionClaimToken: claim.Token, ProjectID: pid, TaskBeadID: claim.BeadID, AgentName: fmt.Sprintf("operator-load-%d", i), OpencodeSessionID: session, SpawnTime: time.Now().Unix()})
		if e != nil {
			t.Fatal(e)
		}
		var accepted bool
		if e = st.DB().QueryRowContext(ctx, `SELECT commander_report_result(?,?,?,?,?)`, pid, claim.BeadID, rid, session, "Final report: isolated fixture evidence passes").Scan(&accepted); e != nil || !accepted {
			t.Fatal("result transfer", e)
		}
		if e = st.MarkClosed(ctx, rid, "exited"); e != nil {
			t.Fatal(e)
		}
		if _, e = st.DB().ExecContext(ctx, `UPDATE bead_tasks SET status='closed' WHERE project_id=? AND id=?`, pid, claim.BeadID); e != nil {
			t.Fatal(e)
		}
	}
	if extra, e := st.ClaimNextExecution(ctx, "load-slot", 1, time.Now()); e != nil || extra != nil {
		t.Fatalf("completed queue not empty: %+v %v", extra, e)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	t.Logf("assignments=%d elapsed=%s claim_p50=%s claim_p95=%s claim_max=%s model_calls=0", len(seen), time.Since(started).Round(time.Millisecond), durations[500], durations[950], durations[999])
	if durations[950] > 2*time.Second {
		t.Fatalf("claim p95 too slow for bounded supervision: %s", durations[950])
	}
}
