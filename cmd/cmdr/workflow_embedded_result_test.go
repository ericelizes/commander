package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/workflowtemplate"
)

// The scenario exercises the real registry reader and proof verifier, without a
// live project, agent, GitHub call, or retrospective receipt.
func TestEmbeddedReviewVerdictScenario(t *testing.T) {
	var scenario struct {
		Cases []string `json:"cases"`
	}
	data, err := os.ReadFile("../../tests/evals/scenarios/embedded-review-verdict.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	for _, name := range scenario.Cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("COMMANDER_HOME", t.TempDir())
			a := workflowtemplate.AddendumAssessment{Original: workflowtemplate.CommentRef{Bead: "review-task"}, Head: strings.Repeat("a", 40), Base: strings.Repeat("b", 40)}
			receipt := typedAddendumResult(t, a, func(r *workflowtemplate.ReviewResult) {
				switch name {
				case "wrong-head":
					r.Head = strings.Repeat("f", 40)
				case "partial":
					r.Coverage = "partial"
				case "findings":
					r.Source = "findings"
					r.Findings = []workflowtemplate.ReviewFinding{{ID: "finding-1", Severity: "major", Summary: "synthetic bug"}}
				}
			})
			originalRuns := workflowResultRuns
			workflowResultRuns = func(_ context.Context, project, task string) (map[int64]string, error) {
				if project != "synthetic" || task != "review-task" {
					t.Fatalf("wrong provenance scope: %s %s", project, task)
				}
				switch name {
				case "missing-run":
					return nil, fmt.Errorf("run missing")
				case "wrong-actor":
					return map[int64]string{typedAddendumRun: "operator-other"}, nil
				}
				return map[int64]string{typedAddendumRun: typedAddendumActor}, nil
			}
			defer func() { workflowResultRuns = originalRuns }()
			var result workflowtemplate.ReviewResult
			if err := json.Unmarshal([]byte(strings.TrimPrefix(receipt, workflowtemplate.ResultPrefix)), &result); err != nil {
				t.Fatal(err)
			}
			registry := filepath.Join(agentrt.CommanderHome(), "review-executions", strconv.Itoa(typedAddendumRun), result.ManifestHash)
			switch name {
			case "missing-manifest":
				if err := os.Remove(registry + ".json"); err != nil {
					t.Fatal(err)
				}
			case "missing-terminal":
				if err := os.Remove(registry + ".terminal.json"); err != nil {
					t.Fatal(err)
				}
			case "changed-manifest":
				if err := os.WriteFile(registry+".json", []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "split-accepted":
				receipt = strings.Replace(receipt, workflowtemplate.ResultPrefix, workflowtemplate.ResultMarker+"\n", 1)
			case "split-blank-line":
				receipt = strings.Replace(receipt, workflowtemplate.ResultPrefix, workflowtemplate.ResultMarker+"\n\n", 1)
			case "split-wrapped":
				receipt = strings.Replace(receipt, workflowtemplate.ResultPrefix+"{", workflowtemplate.ResultMarker+"\n{\n", 1)
			}
			report := "Head: " + a.Head + "\nOriginal source report\n" + receipt + "\n\nCE leaf JSON:\n{\"status\":\"complete\"}"
			if name == "contradiction" {
				report += "\nNOT CLEAN"
			}
			review := workflowtemplate.Review{ID: "review-task", Status: "closed", Owner: typedAddendumActor, Comments: []string{report}}
			err = loadReviewResults(context.Background(), "synthetic", review.ID, &review)
			if err == nil {
				gh := func(context.Context, ...string) ([]byte, error) {
					return []byte(`{"headRefOid":"` + a.Head + `","baseRefOid":"` + a.Base + `","state":"OPEN"}`), nil
				}
				read := func(context.Context, string) (workflowtemplate.Review, error) { return review, nil }
				_, err = workflowtemplate.Verify(context.Background(), gh, read, "review-proof", testPR, "instance", "review", &workflowtemplate.Proof{ReviewBead: review.ID, Head: a.Head, NoFindings: true})
			}
			if (err == nil) != (name == "accepted" || name == "split-accepted") {
				t.Fatalf("result: %v", err)
			}
			if review.Comments[0] != report {
				t.Fatal("original report changed")
			}
		})
	}
}
