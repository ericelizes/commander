package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/gates"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
)

func TestUserGatePassAuthority(t *testing.T) {
	for _, approver := range []string{"user", "sample-owner"} {
		for _, role := range []string{"", "commander", "manager-sample", "operator-sample", "reviewer-sample", "user"} {
			for _, inherited := range []bool{false, true} {
				t.Run(approver+"/"+role+"/inherited="+map[bool]string{true: "yes", false: "no"}[inherited], func(t *testing.T) {
					m := setupGateProject(t)
					t.Setenv("COMMANDER_ROLE", role)
					t.Setenv("COMMANDER_MANAGER_ROLE", "")
					if inherited {
						t.Setenv("COMMANDER_MANAGER_ROLE", "manager-sample")
					}
					m.Seed(beads.Bead{ID: "approval", Status: "open", Description: "Approve exact migration", Labels: []string{gates.KindLabel, "gate-of:ms-1_2e2", "gate-name:approval", "verifier:" + approver, "approver:" + approver}})
					before, _ := json.Marshal(m)
					code, _, msg := gateCmd(t, "pass", "--project", "manifest", "--bead", "ms-1.2", "--name", "approval", "--evidence", "Approved exact migration", "--by", "user")
					allowed := role == "commander" || role == "" && !inherited
					if allowed {
						if code != 0 {
							t.Fatal(msg)
						}
						return
					}
					if code == 0 || !strings.Contains(msg, "only the user or Commander") {
						t.Fatal(code, msg)
					}
					after, _ := json.Marshal(m)
					if string(before) != string(after) {
						t.Fatal("refused pass mutated beads")
					}
					if _, err := checkProdGate(context.Background(), m, "/d", "ms-1.2", "approval", "manager-sample"); err == nil || !strings.Contains(err.Error(), "not closed") {
						t.Fatal("forged pass enabled production", err)
					}
				})
			}
		}
	}
}

func TestCloseParentRefusesUnfinishedChildren(t *testing.T) {
	for _, status := range []string{"open", "in_progress", "blocked", "deferred"} {
		for _, kind := range []string{"kind:objective", "kind:workstream", "kind:task"} {
			t.Run(status+"/"+kind, func(t *testing.T) {
				m := gateMem(t, "commander")
				m.Seed(beads.Bead{ID: "unrelated-id", Status: status, Labels: []string{kind}})
				m.Edges = []beads.Dep{{IssueID: "unrelated-id", DependsOnID: "w", Type: "parent-child"}}
				before, _ := json.Marshal(m.MemClient)
				out, errOut, read := withTempOutputFiles(t)
				code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "w", "--reason", "Ready"}, out, errOut)
				_, msg := read()
				if code == 0 || !strings.Contains(msg, "unfinished children (unrelated-id)") {
					t.Fatal(code, msg)
				}
				after, _ := json.Marshal(m.MemClient)
				if string(before) != string(after) || len(m.Calls) > 0 {
					t.Fatal("refusal wrote state", m.Calls)
				}
			})
		}
	}
}

func TestCloseParentFinishedHistory(t *testing.T) {
	m := gateMem(t, "commander")
	m.Seed(beads.Bead{ID: "history", Status: "closed"})
	m.Seed(beads.Bead{ID: "historic-descendant", Status: "open"})
	m.Edges = []beads.Dep{{IssueID: "history", DependsOnID: "w", Type: "parent-child"}, {IssueID: "historic-descendant", DependsOnID: "history", Type: "parent-child"}}
	if err := requireParentFinished(context.Background(), m, "/d", "w"); err != nil {
		t.Fatal(err)
	}
}

func TestCloseParentPendingReview(t *testing.T) {
	m := gateMem(t, "commander")
	m.Edges = nil
	m.Beads[0].Labels = append(m.Beads[0].Labels, beads.Wait{Kind: beads.WaitReview, Ref: "reviewer-a", Since: time.Unix(1, 0)}.Label(), "until:ready")
	out, errOut, read := withTempOutputFiles(t)
	code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "w"}, out, errOut)
	_, msg := read()
	if code == 0 || !strings.Contains(msg, "Waiting for review") || len(m.Calls) > 0 {
		t.Fatal(code, msg, m.Calls)
	}
}

// The eval calls the actual private command handlers, like retire-legacy.json.
func TestCompletionAuthorityScenario(t *testing.T) {
	raw, err := os.ReadFile("../../tests/evals/scenarios/completion-authority.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Roles         []string
		ChildStatuses []string
	}
	if err = json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	for _, role := range sc.Roles {
		t.Run(role, func(t *testing.T) {
			m := setupGateProject(t)
			m.Seed(beads.Bead{ID: "approval", Status: "open", Description: "Approve sample operation", Labels: gates.Labels(store.Gate{BeadID: "ms-1.2", Name: "approval", Verifier: store.VerifierUser})})
			t.Setenv("COMMANDER_ROLE", role)
			code, _, msg := gateCmd(t, "pass", "--project", "manifest", "--bead", "ms-1.2", "--name", "approval", "--evidence", "claimed approval", "--by", "user")
			if code == 0 || !strings.Contains(msg, "only the user or Commander") {
				t.Fatal(code, msg)
			}
			fmt.Printf("Refused user-gate pass by %s with --by user; gate remains open\n", role)
			b, _ := m.Show(context.Background(), "/d", "approval")
			if b.Status != "open" {
				t.Fatal("gate disappeared")
			}
		})
	}
	for _, status := range sc.ChildStatuses {
		t.Run(status, func(t *testing.T) {
			m := gateMem(t, "commander")
			m.Seed(beads.Bead{ID: "nested", Title: "Nested sample", Status: status, Labels: []string{"kind:objective", "kind:workstream"}})
			m.Edges = []beads.Dep{{IssueID: "nested", DependsOnID: "w", Type: "parent-child"}}
			out, errOut, read := withTempOutputFiles(t)
			code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "w"}, out, errOut)
			_, msg := read()
			if code == 0 || !strings.Contains(msg, "unfinished children") {
				t.Fatal(code, msg)
			}
			st, cleanup, err := needsYouStoreFn()
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			snapshot := livedata.BuildSnapshot(livedata.Load(context.Background(), livedata.Deps{Store: st, Beads: m}), nil)
			raw, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			fmt.Printf("Refused close: parent Notify and child Nested sample (%s) retained in fresh snapshot\n", status)
			for _, title := range []string{"Notify", "Nested sample"} {
				if !strings.Contains(string(raw), title) {
					t.Fatalf("lost %s from snapshot: %s", title, raw)
				}
			}
			for _, id := range []string{"w", "nested"} {
				b, _ := m.Show(context.Background(), "/d", id)
				if b.Status == "closed" {
					t.Fatal("lost visible work", id)
				}
			}
		})
	}
}

type completionReadFailure struct {
	beads.Client
	fail string
}

func (c completionReadFailure) Show(ctx context.Context, dir, id string) (*beads.Bead, error) {
	if c.fail == id {
		return nil, fmt.Errorf("synthetic unreadable bead")
	}
	return c.Client.Show(ctx, dir, id)
}
func (c completionReadFailure) Dependents(ctx context.Context, dir, id string) ([]beads.Dep, error) {
	if c.fail == "edges" {
		return nil, fmt.Errorf("synthetic unreadable edges")
	}
	return c.Client.Dependents(ctx, dir, id)
}
func TestCloseParentUnreadableRefuses(t *testing.T) {
	for _, fail := range []string{"w", "w.2", "edges"} {
		t.Run(fail, func(t *testing.T) {
			m := gateMem(t, "commander")
			c := completionReadFailure{Client: m, fail: fail}
			newBeadsClientFn = func() beads.Client { return c }
			out, errOut, read := withTempOutputFiles(t)
			code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "w"}, out, errOut)
			_, msg := read()
			if code == 0 || !strings.Contains(msg, "cannot") || len(m.Calls) > 0 {
				t.Fatal(code, msg, m.Calls)
			}
		})
	}
}
