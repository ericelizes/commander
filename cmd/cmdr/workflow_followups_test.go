package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/workflowtemplate"
)

// TestReviewMinorPolicyScenario replays scenarios/review-minor-policy.json
// through the real close door (checkWorkflowClose), the real typed-receipt
// registry reader and the follow-up filer, on an in-memory beads graph and a
// scripted GitHub: no live project, agent or GitHub call.
func TestReviewMinorPolicyScenario(t *testing.T) {
	var scenario struct {
		Rounds []struct {
			Name     string                           `json:"name"`
			Step     int                              `json:"step"`
			Retry    bool                             `json:"retry"`
			Renew    bool                             `json:"renew"`
			Compare  json.RawMessage                  `json:"compare"`
			Head     string                           `json:"head"`
			Review   string                           `json:"review"`
			Base     string                           `json:"base"`
			Findings []workflowtemplate.ReviewFinding `json:"findings"`
			Proof    workflowtemplate.Proof           `json:"proof"`
			Want     string                           `json:"want"`
			Follow   int                              `json:"follow_ups"`
		} `json:"rounds"`
	}
	data, err := os.ReadFile("../../tests/evals/scenarios/review-minor-policy.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	sha := map[string]string{"h1": strings.Repeat("1", 40), "h2": strings.Repeat("2", 40), "fix": strings.Repeat("3", 40), "h3": strings.Repeat("4", 40), "fix2": strings.Repeat("5", 40), "pr-base": strings.Repeat("b", 40)}
	at := func(s string) string {
		if v, ok := sha[s]; ok {
			return v
		}
		return s
	}

	t.Setenv("COMMANDER_HOME", t.TempDir())
	ctx := context.Background()
	mem := &beads.MemClient{}
	res := seedReviewWorkflow(t, mem)
	head := sha["h1"]
	var compare []byte
	pinWorkflowGH(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "pr" {
			var commits []string
			for _, k := range []string{"h1", "fix", "h2", "fix2", "h3"} {
				commits = append(commits, `{"oid":"`+sha[k]+`"}`)
			}
			return []byte(`{"headRefOid":"` + head + `","baseRefOid":"` + sha["pr-base"] + `","state":"OPEN","commits":[` + strings.Join(commits, ",") + `]}`), nil
		}
		if args[0] == "api" && strings.Contains(args[1], "/compare/") {
			return compare, nil
		}
		return []byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}}`), nil
	})
	old := workflowResultRuns
	workflowResultRuns = func(context.Context, string, string) (map[int64]string, error) {
		return map[int64]string{typedAddendumRun: typedAddendumActor}, nil
	}
	t.Cleanup(func() { workflowResultRuns = old })
	seedWriterBinding(mem, res.Container, head)

	followUps := func() []beads.Bead {
		all, _ := mem.List(ctx, "/d", beads.ListOptions{Status: "all"})
		var out []beads.Bead
		for _, b := range all {
			if workflowtemplate.Label(b, followUpLabel) != "" {
				out = append(out, b)
			}
		}
		return out
	}
	steps := res.Remaining
	if err := checkWorkflowClose(ctx, mem, "/d", "p", steps[0].BeadID); err != nil {
		t.Fatal(err)
	}
	mem.Close(ctx, "/d", steps[0].BeadID)
	current := 1
	for _, r := range scenario.Rounds {
		// Moving on to the next step closes the one before it.
		if r.Step != current {
			mem.Close(ctx, "/d", steps[current].BeadID)
			current = r.Step
		}
		compare = r.Compare
		if !r.Retry {
			if h := at(r.Head); h != head {
				head = h
				seedWriterBinding(mem, res.Container, head)
			}
		}
		if !r.Retry && !r.Renew {
			mem.Seed(beads.Bead{ID: r.Review, Status: "closed"})
			mem.SetParent(ctx, "/d", r.Review, "B")
			mem.AddComment(ctx, "/d", r.Review, "Head: "+head+"\nreport\nCLEAN")
			a := workflowtemplate.AddendumAssessment{Original: workflowtemplate.CommentRef{Bead: r.Review}, Head: head, Base: at(r.Base)}
			mem.AddComment(ctx, "/d", r.Review, typedAddendumResult(t, a, func(res *workflowtemplate.ReviewResult) {
				if len(r.Findings) > 0 {
					res.Source, res.Findings = "findings", r.Findings
				}
			}))
		}
		if !r.Retry {
			p := r.Proof
			p.ReviewBead, p.Head, p.PriorHead = r.Review, head, at(p.PriorHead)
			for i := range p.FixCommits {
				p.FixCommits[i] = at(p.FixCommits[i])
			}
			raw, _ := json.Marshal(p)
			mem.AddComment(ctx, "/d", steps[r.Step].BeadID, workflowtemplate.ProofPrefix+string(raw))
		}
		err := checkWorkflowClose(ctx, mem, "/d", "p", steps[r.Step].BeadID)
		if r.Want == "pass" && err != nil {
			t.Fatalf("%s: refused: %v", r.Name, err)
		}
		if r.Want != "pass" && (err == nil || !strings.Contains(err.Error(), r.Want)) {
			t.Fatalf("%s: want refusal %q, got %v", r.Name, r.Want, err)
		}
		if got := len(followUps()); got != r.Follow {
			t.Fatalf("%s: want %d follow-ups, got %d", r.Name, r.Follow, got)
		}
		if r.Want == "pass" && !r.Retry {
			cs, _ := mem.Comments(ctx, "/d", steps[r.Step].BeadID)
			e, ok := workflowtemplate.ParseEvidence(cs[len(cs)-1].Text)
			if r.Renew && (!ok || !strings.Contains(e.Finding, "delta renewal of "+sha[r.Proof.PriorHead]) || e.Head != head) {
				t.Fatalf("%s: renewal not recorded: %+v", r.Name, e)
			}
			if !r.Renew && (!ok || len(e.FollowUps) != len(r.Findings) || (len(r.Findings) > 0 && !strings.Contains(e.Finding, workflowtemplate.MinorPolicy))) {
				t.Fatalf("%s: evidence does not record the policy: %+v", r.Name, e)
			}
		}
	}
	for _, b := range followUps() {
		full, _ := mem.Show(ctx, "/d", b.ID)
		if full.ParentID != "B" || !hasLabel(full.Labels, followUpOfLabel+beads.EscapeLabel(res.Container)) || !hasLabel(full.Labels, "kind:objective") {
			t.Fatalf("follow-up %s not under the workstream or not linked to the block: %+v", b.ID, full)
		}
		if !holdingWait(full.Labels) || !strings.Contains(full.Description, "manager-B") {
			t.Fatalf("follow-up %s is not held by the owning Manager: %+v", b.ID, full)
		}
	}
	linked := 0
	for _, e := range mem.Edges {
		if e.Type == "discovered-from" && e.DependsOnID == res.Container {
			linked++
		}
	}
	if linked != len(followUps()) {
		t.Fatalf("%d of %d follow-ups linked to the block", linked, len(followUps()))
	}
}
