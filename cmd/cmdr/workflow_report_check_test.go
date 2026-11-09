package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/workflowtemplate"
)

// TestReviewReportCheckScenario runs scenarios/review-report-check.json through
// the real verb and the real sealed launch registry (typedAddendumResult seeds it).
func TestReviewReportCheckScenario(t *testing.T) {
	var scenario struct {
		Cases []struct {
			Name        string `json:"name"`
			Findings    string `json:"findings"`
			Verdict     string `json:"verdict"`
			DropHead    bool   `json:"drop_head"`
			HeadLine    string `json:"head_line"`
			NoReceipt   bool   `json:"no_receipt"`
			Manifest    string `json:"manifest"`
			ReceiptHead string `json:"receipt_head"`
			Blocked     bool   `json:"blocked"`
			Want        int    `json:"want"`
			Says        string `json:"says"`
		} `json:"cases"`
	}
	data, err := os.ReadFile("../../tests/evals/scenarios/review-report-check.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("a", 40)
	for _, c := range scenario.Cases {
		t.Run(c.Name, func(t *testing.T) {
			t.Setenv("COMMANDER_HOME", t.TempDir())
			a := workflowtemplate.AddendumAssessment{Original: workflowtemplate.CommentRef{Bead: "review-task"}, Head: head, Base: strings.Repeat("b", 40)}
			receipt := typedAddendumResult(t, a, func(r *workflowtemplate.ReviewResult) {
				if c.Findings != "" {
					r.Source, r.Findings = "findings", []workflowtemplate.ReviewFinding{{ID: "f1", Severity: c.Findings, Summary: "synthetic"}}
				}
				if c.Manifest != "" {
					r.ManifestHash = strings.Repeat(c.Manifest, 64)
				}
				if c.ReceiptHead != "" {
					r.Head = strings.Repeat(c.ReceiptHead, 40)
				}
				if c.Blocked {
					r.Execution, r.Coverage, r.Source, r.Reason = "blocked", "partial", "not-assessed", "provider quota"
				}
			})
			verdict := "CLEAN"
			if c.Verdict != "" {
				verdict = c.Verdict
			}
			headLine := "Head: " + head
			if c.HeadLine != "" {
				headLine = "Head: " + strings.Repeat(c.HeadLine, 40)
			}
			if c.DropHead {
				headLine = "Reviewed the branch tip."
			}
			dir := t.TempDir()
			reportFile := filepath.Join(dir, "report.md")
			os.WriteFile(reportFile, []byte(headLine+"\nBase: "+a.Base+"\nfindings listed above\n"+verdict+"\n"), 0o600)
			args := []string{"review-report-check", "--head", head, "--file", reportFile}
			if !c.NoReceipt {
				receiptFile := filepath.Join(dir, "receipt.txt")
				os.WriteFile(receiptFile, []byte(receipt), 0o600)
				args = append(args, "--receipt", receiptFile)
			}
			stdout, stderr, read := withTempOutputFiles(t)
			code := runWorkflow(args, stdout, stderr)
			out, errOut := read()
			if code != c.Want || !strings.Contains(out+errOut, c.Says) {
				t.Fatalf("exit %d (want %d), output %q %q, want it to say %q", code, c.Want, out, errOut, c.Says)
			}
		})
	}
}
