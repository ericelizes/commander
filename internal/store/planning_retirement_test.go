package store

import (
	"context"
	"errors"
	"testing"
)

func retirementFixture(t *testing.T) *Store {
	t.Helper()
	s, req, _ := consolidationFixture(t)
	if _, err := s.ConsolidateLegacyPlanning(context.Background(), "", req); err != nil {
		t.Fatal(err)
	}
	c, err := s.OrchestrationControl(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetOrchestrationControl(context.Background(), c.Revision, "active", "retirement fixture"); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPlanningRetirementRequiresConsolidationAndNoGlobalCustody(t *testing.T) {
	for _, kind := range []string{"unknown", "explicit-owner", "other-project", "raw-execution", "normalized-execution", "native-execution"} {
		t.Run(kind, func(t *testing.T) {
			s := retirementFixture(t)
			role := "manager-home"
			switch kind {
			case "unknown":
				role = "manager-unknown"
			case "explicit-owner":
				role = "manager-kept"
			case "other-project":
				if _, err := s.DB().Exec(`INSERT INTO projects(id,name,path,type,status,activated_at) VALUES(43,'other','/other','code','active',1); INSERT INTO jurisdiction_projects(project_id,default_commander) VALUES(43,'manager-home')`); err != nil {
					t.Fatal(err)
				}
			default:
				name := role
				driver := ""
				if kind == "normalized-execution" {
					name = "manager.home"
				}
				if kind == "native-execution" {
					driver = "native"
				}
				if _, err := s.DB().Exec(`UPDATE agent_runs SET agent_name=?,execution_driver=? WHERE id=17`, name, driver); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			done, err := s.RetirePlanningRuntime(context.Background(), role, func(context.Context) (string, error) { called = true; return "must-not-stop", nil })
			if err != nil || done || called {
				t.Fatal("retired owned or unproven runtime", done, called, err)
			}
		})
	}
}

func TestPlanningRetirementIsBoundedIdempotentAndKeepsWork(t *testing.T) {
	s := retirementFixture(t)
	ctx := context.Background()
	before, err := s.GetAgentRun(ctx, 17)
	if err != nil {
		t.Fatal(err)
	}
	stopErr := errors.New("host identity cannot be verified")
	done, err := s.RetirePlanningRuntime(ctx, "manager-home", func(context.Context) (string, error) { return "", stopErr })
	if done || !errors.Is(err, stopErr) {
		t.Fatal("failed attestation was recorded", done, err)
	}
	calls := 0
	stop := func(context.Context) (string, error) { calls++; return "retained-session", nil }
	done, err = s.RetirePlanningRuntime(ctx, "manager-home", stop)
	if err != nil || !done || calls != 1 {
		t.Fatal(done, calls, err)
	}
	done, err = s.RetirePlanningRuntime(ctx, "manager-home", stop)
	if err != nil || done || calls != 1 {
		t.Fatal("replayed physical retirement", done, calls, err)
	}
	after, err := s.GetAgentRun(ctx, 17)
	if err != nil || after.Status != before.Status || after.OpencodeSessionID != before.OpencodeSessionID {
		t.Fatal("changed execution", after, err)
	}
	var session string
	if err = s.DB().QueryRow(`SELECT session_id FROM planning_runtime_retirements WHERE agent_id='manager-home'`).Scan(&session); err != nil || session != "retained-session" {
		t.Fatal(session, err)
	}
	roles, err := s.PlanningRetirementCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range roles {
		if role == "manager-home" {
			t.Fatal("retired host remains scheduled")
		}
	}
}

func TestPlanningRetirementFencesConcurrentExecutionAndJurisdiction(t *testing.T) {
	s := retirementFixture(t)
	ctx := context.Background()
	done, err := s.RetirePlanningRuntime(ctx, "manager-home", func(context.Context) (string, error) {
		for _, statement := range []string{
			`UPDATE agent_runs SET agent_name='manager-home' WHERE id=17`,
			`UPDATE jurisdiction_projects SET default_commander='manager-home' WHERE project_id=42`,
			`INSERT INTO commander_jurisdictions(project_id,bead_id,commander_id,origin) VALUES(42,'home','manager-home','explicit')`,
		} {
			conn, e := s.DB().Conn(ctx)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = conn.ExecContext(ctx, `SET lock_timeout='80ms'`); e != nil {
				t.Fatal(e)
			}
			_, e = conn.ExecContext(ctx, statement)
			_, _ = conn.ExecContext(ctx, `RESET lock_timeout`)
			conn.Close()
			if e == nil {
				t.Fatalf("custody changed during physical retirement: %s", statement)
			}
		}
		return "fenced-session", nil
	})
	if err != nil || !done {
		t.Fatal(done, err)
	}
}
