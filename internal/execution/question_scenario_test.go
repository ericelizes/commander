package execution

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/supervision"
)

func TestCommanderQuestionCheckpointScenario(t *testing.T) {
	var scenario struct {
		Question, Recommendation, Checkpoint, Answer string
		ExpectedOwner                                string `json:"expected_owner"`
		Deliveries                                   int    `json:"expected_question_deliveries"`
		Launches                                     int    `json:"expected_execution_launches"`
		UserCards                                    int    `json:"expected_user_cards"`
	}
	raw, e := os.ReadFile(filepath.Join("..", "..", "tests", "evals", "scenarios", "commander-question-checkpoint.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &scenario); e != nil {
		t.Fatal(e)
	}
	st, pid, ctx := fixture(t)
	if _, e = st.DB().ExecContext(ctx, `UPDATE bead_tasks SET status='deferred',execution_ready=FALSE WHERE project_id=? AND id='two'`, pid); e != nil {
		t.Fatal(e)
	}
	var firstName, questionID string
	var priorRun, currentRun int64
	launches := 0
	adapter := fakeSpawner(func(ctx context.Context, r spawn.SpawnRequest) (*spawn.SpawnResult, error) {
		launches++
		if launches > 1 && (!r.Resume || r.Name != firstName || r.ResumeSourceRunID != priorRun || r.ResumeSessionID != firstName || !strings.Contains(r.ResumeNote, "question history")) {
			t.Fatalf("checkpoint abandoned for a fresh execution: %+v", r)
		}
		result, e := recordRun(st, ctx, r)
		if e != nil {
			return nil, e
		}
		currentRun = result.RunID
		if launches == 1 {
			firstName = r.Name
			priorRun = result.RunID
			var revision int64
			if e = st.DB().QueryRowContext(ctx, `SELECT plan_revision FROM bead_tasks WHERE project_id=? AND id=?`, pid, r.TaskBeadID).Scan(&revision); e != nil {
				return nil, e
			}
			questionID, e = st.AskQuestion(ctx, store.QuestionRequest{ProjectID: pid, BeadID: r.TaskBeadID, PlanRevision: revision, Actor: "operator-" + r.Name, Session: r.Name, RunID: result.RunID, ClientKey: "integration-decision", Question: scenario.Question, Recommendation: scenario.Recommendation, Checkpoint: scenario.Checkpoint, CheckpointRuntime: "claude", Blocking: true})
			if e != nil {
				return nil, e
			}
			if e = st.MarkClosed(ctx, result.RunID, "exited"); e != nil {
				return nil, e
			}
		}
		return result, nil
	})
	worker := &Worker{Store: st, Spawner: adapter, Capacity: 1}
	if e = worker.PassSlot(ctx, "vm-0"); e != nil {
		t.Fatal(e)
	}
	// A restart before an answer must neither lose the question nor launch a
	// replacement into the same blocked intent.
	restarted := &Worker{Store: st, Spawner: adapter, Capacity: 1}
	if e = restarted.PassSlot(ctx, "vm-0"); e != nil {
		t.Fatal(e)
	}
	if launches != 1 {
		t.Fatal("pending question was treated as a crash")
	}
	deliveries := 0
	reviewer := &supervision.Worker{Store: st, Mode: "active", Deliver: func(ctx context.Context, role, prompt string) error {
		deliveries++
		if role != scenario.ExpectedOwner || !strings.Contains(prompt, "question inbox") {
			t.Fatalf("question routed incorrectly: %s %s", role, prompt)
		}
		inbox, e := st.QuestionInbox(ctx, role)
		if e != nil || len(inbox) != 1 || inbox[0].ID != questionID {
			t.Fatalf("durable inbox lost question: %+v %v", inbox, e)
		}
		var epoch int64
		var request string
		if e = st.DB().QueryRowContext(ctx, `SELECT epoch,request_id FROM bead_supervision WHERE project_id=? AND bead_id='one'`, pid).Scan(&epoch, &request); e != nil {
			return e
		}
		if e = st.AcknowledgeSupervision(ctx, pid, "one", epoch, role, request); e != nil {
			return e
		}
		complete, e := st.AnswerQuestion(ctx, questionID, role, scenario.Answer, true)
		if e != nil || !complete {
			t.Fatalf("answer did not resolve request: %t %v", complete, e)
		}
		return nil
	}}
	if e = reviewer.Pass(ctx); e != nil {
		t.Fatal(e)
	}
	if e = restarted.PassSlot(ctx, "vm-0"); e != nil {
		t.Fatal(e)
	}
	if deliveries != scenario.Deliveries || launches != scenario.Launches || currentRun == priorRun {
		t.Fatalf("wrong handoff: deliveries=%d launches=%d runs=%d/%d", deliveries, launches, priorRun, currentRun)
	}
	old, e := st.GetAgentRun(ctx, priorRun)
	if e != nil {
		t.Fatal(e)
	}
	next, e := st.GetAgentRun(ctx, currentRun)
	if e != nil || old.OpencodeSessionID != next.OpencodeSessionID {
		t.Fatal("resumption lost the original session", e)
	}
	var cards int
	if e = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM needs_you`).Scan(&cards); e != nil || cards != scenario.UserCards {
		t.Fatalf("internal question became a user card: %d %v", cards, e)
	}
}
