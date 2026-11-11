package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPostgresActiveExecutionCannotBypassCapacityAdmission(t *testing.T) {
	var scenario struct{ Roles []string }
	raw, e := os.ReadFile(filepath.Join("..", "..", "tests", "evals", "scenarios", "commander-execution-admission.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &scenario); e != nil || len(scenario.Roles) != 4 {
		t.Fatal("missing admission matrix", e)
	}
	for _, role := range scenario.Roles {
		t.Run(role, func(t *testing.T) {
			st, pid, ctx := executionFixture(t)
			var before int
			if e := st.DB().QueryRow(`SELECT count(*) FROM agent_runs`).Scan(&before); e != nil {
				t.Fatal(e)
			}
			for _, bead := range []string{"leaf", "ordinary"} {
				if e := st.CheckExecutionAdmission(ctx, pid, bead, ""); e == nil {
					t.Fatal("early preflight accepted an unreserved launch")
				}
				if _, e := st.InsertAgentRun(ctx, AgentRun{ProjectID: pid, TaskBeadID: bead, AgentName: role, OpencodeSessionID: "unreserved", SpawnTime: time.Now().Unix()}); e == nil || !strings.Contains(e.Error(), "capacity claim") {
					t.Fatal("unreserved launch bypassed active orchestration", e)
				}
			}
			var after int
			if e := st.DB().QueryRow(`SELECT count(*) FROM agent_runs`).Scan(&after); e != nil || after != before {
				t.Fatal("refused launch mutated execution history", before, after, e)
			}
			if e := st.CheckExecutionAdmission(ctx, pid, "leaf", "invented-token"); e == nil {
				t.Fatal("invented reservation passed preflight")
			}
			claim, e := st.ClaimNextExecution(ctx, "vm-0", 1, time.Now())
			if e != nil || claim == nil {
				t.Fatal(claim, e)
			}
			if e := st.CheckExecutionAdmission(ctx, pid, claim.BeadID, claim.Token); e != nil {
				t.Fatal("actual reservation failed preflight", e)
			}
			other := "leaf"
			if claim.BeadID == other {
				other = "ordinary"
			}
			if e := st.CheckExecutionAdmission(ctx, pid, other, claim.Token); e == nil {
				t.Fatal("another bead borrowed reservation")
			}

			if _, e := st.InsertAgentRun(ctx, AgentRun{ExecutionClaimToken: claim.Token, ProjectID: pid, TaskBeadID: claim.BeadID, AgentName: role, OpencodeSessionID: "reserved", SpawnTime: time.Now().Unix()}); e != nil {
				t.Fatal("canonical admission refused", e)
			}
			if extra, e := st.ClaimNextExecution(ctx, "vm-1", 1, time.Now()); e != nil || extra != nil {
				t.Fatal("capacity not enforced after canonical binding", extra, e)
			}
		})
	}
}

func TestPostgresAdmissionFencesConcurrentModeChanges(t *testing.T) {
	for _, operation := range []string{"bind", "reserve"} {
		t.Run(operation, func(t *testing.T) {
			st, pid, ctx := executionFixture(t)
			// Block the mode row while its old committed version is active. A reserve
			// caller can observe active outside its transaction, but must recheck after
			// this disabling transaction commits. Binding has the same serialization.
			tx, e := st.DB().BeginTx(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback()
			var blocker int
			if e = tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); e != nil {
				t.Fatal(e)
			}
			if _, e = tx.ExecContext(ctx, `UPDATE orchestration_control SET mode='off',revision=revision+1 WHERE id=1`); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() {
				if operation == "bind" {
					_, e := st.InsertAgentRun(ctx, AgentRun{ProjectID: pid, TaskBeadID: "leaf", AgentName: "operator-racing", OpencodeSessionID: "racing", SpawnTime: time.Now().Unix()})
					done <- e
				} else {
					claim, e := st.ClaimNextExecution(ctx, "vm-0", 1, time.Now())
					if e == nil && claim != nil {
						e = sql.ErrNoRows
					}
					done <- e
				}
			}()
			deadline := time.Now().Add(5 * time.Second)
			blocked := false
			for time.Now().Before(deadline) {
				if e = st.DB().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE ?=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&blocked); e != nil {
					t.Fatal(e)
				}
				if blocked {
					break
				}
				select {
				case e := <-done:
					t.Fatal("admission did not wait for mode transaction", e)
				default:
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !blocked {
				t.Fatal("did not observe admission waiting on the mode fence")
			}
			if e = tx.Commit(); e != nil {
				t.Fatal(e)
			}
			select {
			case e := <-done:
				if operation == "bind" && (e == nil || !strings.Contains(e.Error(), "disabled")) {
					t.Fatal("binding crossed disable fence", e)
				}
				if operation == "reserve" && e != nil {
					t.Fatal("reservation crossed disable fence", e)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("admission stayed blocked after mode commit")
			}
			var runs, claims int
			if e = st.DB().QueryRowContext(ctx, `SELECT (SELECT count(*) FROM agent_runs),(SELECT count(*) FROM execution_slots WHERE claim_token<>'')`).Scan(&runs, &claims); e != nil || runs != 0 || claims != 0 {
				t.Fatal("disabled launch left custody", runs, claims, e)
			}
		})
	}
}

func TestPostgresShadowImportsRemainReadableButCannotLaunchAfterActivation(t *testing.T) {
	st, pid, ctx := jurisdictionFixture(t)
	old, e := st.InsertAgentRun(ctx, AgentRun{ProjectID: pid, TaskBeadID: "leaf", AgentName: "operator-imported", OpencodeSessionID: "retained", SpawnTime: time.Now().Unix()})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.SetOrchestrationControl(ctx, 1, "active", "test imported custody across activation"); e != nil {
		t.Fatal(e)
	}
	current, e := st.GetAgentRun(ctx, old)
	if e != nil || current.Status != "running" || current.OpencodeSessionID != "retained" {
		t.Fatal("activation discarded imported execution", current, e)
	}
	if _, e = st.InsertAgentRun(ctx, AgentRun{ProjectID: pid, TaskBeadID: "ordinary", AgentName: "operator-imported-next", OpencodeSessionID: "unreserved"}); e == nil {
		t.Fatal("import compatibility became a fresh launch bypass")
	}
	if e = st.MarkClosed(context.Background(), old, "exited"); e != nil {
		t.Fatal("active mode prevented imported lifecycle cleanup", e)
	}
}

func TestPostgresNativeCheckpointBindingCannotChangeProviderOrAccount(t *testing.T) {
	st, pid, ctx := executionFixture(t)
	now := time.Now()
	claim, e := st.ClaimNextExecution(ctx, "vm-0", 1, now)
	if e != nil || claim == nil {
		t.Fatal(claim, e)
	}
	first := AgentRun{ExecutionClaimToken: claim.Token, ProjectID: pid, TaskBeadID: claim.BeadID, AgentName: "operator-native-source", OpencodeSessionID: "native-source-session", SpawnTime: now.Unix(), ExecutionDriver: "native", ExecutionRuntime: "claude", ExecutionAccount: "default", ExecutionModel: "haiku"}
	rid, e := st.InsertAgentRun(ctx, first)
	if e != nil {
		t.Fatal(e)
	}
	loaded, e := st.GetAgentRun(ctx, rid)
	if e != nil || loaded.ExecutionDriver != "native" || loaded.ExecutionRuntime != "claude" || loaded.ExecutionAccount != "default" || loaded.ExecutionModel != "haiku" {
		t.Fatal("source runtime binding was lost by store getter", loaded, e)
	}
	var revision int64
	if e = st.DB().QueryRow(`SELECT plan_revision FROM bead_tasks WHERE project_id=? AND id=?`, pid, first.TaskBeadID).Scan(&revision); e != nil {
		t.Fatal(e)
	}
	question, e := st.AskQuestion(ctx, QuestionRequest{ProjectID: pid, BeadID: first.TaskBeadID, PlanRevision: revision, Actor: first.AgentName, RunID: rid, Session: first.OpencodeSessionID, ClientKey: "native-binding", Question: "Which bounded choice?", Checkpoint: "Prepared checkpoint", CheckpointRuntime: "claude", Blocking: true})
	if e != nil {
		t.Fatal(e)
	}
	if e = st.FinishNativeExecutionReport(ctx, first.AgentName, rid, first.OpencodeSessionID, "exited", "Paused with prepared work for a planning answer"); e != nil {
		t.Fatal(e)
	}
	if _, e = st.AnswerQuestion(ctx, question, "commander", "Continue the prepared work", true); e != nil {
		t.Fatal(e)
	}
	if e = st.ReapExecutionStartupClaims(ctx, now); e != nil {
		t.Fatal(e)
	}
	next, e := st.ClaimNextExecution(ctx, "vm-0", 1, now)
	if e != nil || next == nil || next.ResumeRunID != rid {
		t.Fatal("checkpoint capacity not available", next, e)
	}
	first.ExecutionClaimToken = next.Token
	first.ResumeSourceRunID = rid
	for _, change := range []string{"runtime", "account"} {
		bad := first
		if change == "runtime" {
			bad.ExecutionRuntime = "codex"
		} else {
			bad.ExecutionAccount = "another"
		}
		if _, e = st.InsertAgentRun(ctx, bad); e == nil {
			t.Fatal("checkpoint silently moved its recorded " + change)
		}
	}
	if _, e = st.InsertAgentRun(ctx, first); e != nil {
		t.Fatal("refused mutations consumed the valid checkpoint claim", e)
	}
}
