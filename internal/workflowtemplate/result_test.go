package workflowtemplate

import (
	"context"
	"encoding/json"
	"github.com/example-user/commander/internal/reviewpolicy"
	"github.com/example-user/commander/internal/reviewrun"
	"strings"
	"testing"
)

func testResult() ReviewResult {
	return ReviewResult{Task: "review-task", RunID: 7, Actor: "operator-reviewer", Head: strings.Repeat("a", 40), Base: strings.Repeat("b", 40), ScopeHash: strings.Repeat("c", 64), ManifestHash: strings.Repeat("d", 64), Depth: "focused", Execution: "complete", Source: "clear", Coverage: "complete", Findings: []ReviewFinding{}}
}

func testRecorded() map[string]reviewrun.Recorded {
	r := testResult()
	return map[string]reviewrun.Recorded{r.ManifestHash: {Manifest: reviewrun.Manifest{Version: "review-run@v1", Task: r.Task, RunID: r.RunID, Actor: r.Actor, Policy: reviewpolicy.Result{Base: r.Base, Head: r.Head, ScopeHash: r.ScopeHash, Depth: r.Depth}}, Terminal: reviewrun.Result{Execution: "complete", Exit: 0}}}
}

func TestReviewExecutionRecoveryPreservesSourceFindings(t *testing.T) {
	complete := testResult()
	blocked := complete
	blocked.Execution = "blocked"
	blocked.Coverage = "partial"
	blocked.Source = "not-assessed"
	blocked.Reason = "provider quota exhausted"
	base := Review{ID: complete.Task, Status: "closed", Owner: complete.Actor, Runs: map[int64]string{7: complete.Actor}, Recorded: testRecorded()}
	base.Comments = []string{blocked.Comment()}
	if err := reviewVerdict(base, complete.Head); err == nil || !strings.Contains(err.Error(), "resume only missing") {
		t.Fatalf("blocked attempt accepted/misclassified: %v", err)
	}
	base.Comments = append(base.Comments, complete.Comment())
	if err := reviewVerdict(base, complete.Head); err != nil {
		t.Fatalf("genuine same-head recovery refused: %v", err)
	}
	bug := blocked
	bug.Source = "findings"
	bug.Findings = []ReviewFinding{{ID: "finding-1", Severity: "major", Summary: "incorrect cancellation"}}
	base.Comments = []string{bug.Comment(), complete.Comment()}
	if err := reviewVerdict(base, complete.Head); err == nil || !strings.Contains(err.Error(), "source findings") {
		t.Fatalf("unresolved same-head source finding erased: %v", err)
	}
	base.Comments = []string{"Head: " + complete.Head + "\nNOT CLEAN", complete.Comment()}
	if err := reviewVerdict(base, complete.Head); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous legacy failure overwritten: %v", err)
	}
}

func TestReviewResultRequiresActualRunAndCoherentCoverage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ReviewResult)
	}{
		{"foreign task", func(r *ReviewResult) { r.Task = "other" }},
		{"invented run", func(r *ReviewResult) { r.RunID = 8 }},
		{"writer actor", func(r *ReviewResult) { r.Actor = "operator-writer" }},
		{"bad scope", func(r *ReviewResult) { r.ScopeHash = "none" }},
		{"bad manifest", func(r *ReviewResult) { r.ManifestHash = "none" }},
		{"missing source", func(r *ReviewResult) { r.Source = "" }},
		{"failed complete coverage", func(r *ReviewResult) { r.Execution = "failed"; r.Reason = "timeout" }},
		{"unassessed complete", func(r *ReviewResult) { r.Source = "not-assessed" }},
		{"findings missing", func(r *ReviewResult) { r.Source = "findings" }},
		{"hidden findings", func(r *ReviewResult) { r.Findings = []ReviewFinding{{ID: "x", Severity: "major", Summary: "bug"}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testResult()
			tc.change(&r)
			review := Review{ID: "review-task", Runs: map[int64]string{7: "operator-reviewer"}, Recorded: testRecorded(), Comments: []string{r.Comment()}}
			if err := reviewVerdict(review, testResult().Head); err == nil {
				t.Fatal("invalid receipt accepted")
			}
		})
	}
}

func TestTypedReceiptIsHeadBoundAndCannotHideMalformedResult(t *testing.T) {
	r := testResult()
	review := Review{ID: r.Task, Runs: map[int64]string{7: r.Actor}, Recorded: testRecorded(), Comments: []string{r.Comment()}}
	if err := reviewVerdict(review, strings.Repeat("e", 40)); err == nil {
		t.Fatal("stale result accepted")
	}
	review.Comments = append(review.Comments, ResultPrefix+"{broken")
	if err := reviewVerdict(review, r.Head); err == nil {
		t.Fatal("malformed terminal receipt ignored")
	}
	review.Comments = append(review.Comments, r.Comment())
	if err := reviewVerdict(review, r.Head); err != nil {
		t.Fatalf("valid later result could not repair malformed execution record: %v", err)
	}
}

func TestMinorFindingDoesNotRequireSourceChangeOrNewReview(t *testing.T) {
	r := testResult()
	r.Source = "findings"
	r.Findings = []ReviewFinding{{ID: "minor-1", Severity: "minor", Summary: "optional naming suggestion"}}
	review := Review{ID: r.Task, Status: "closed", Owner: r.Actor, Runs: map[int64]string{7: r.Actor}, Recorded: testRecorded(), Comments: []string{r.Comment()}}
	if err := reviewVerdict(review, r.Head); err != nil {
		t.Fatalf("minor-only completed review blocked: %v", err)
	}
	read := func(context.Context, string) (Review, error) { return review, nil }
	proof := &Proof{ReviewBead: r.Task, Head: r.Head, NoFixRequired: true}
	if _, err := Verify(context.Background(), fakeGH(r.Head, false), read, "review-proof", testURL, "key", "step", proof); err != nil {
		t.Fatalf("minor-only review required fake fix commit: %v", err)
	}
	complete := testResult()
	review.Comments = append(review.Comments, complete.Comment())
	if err := reviewVerdict(review, r.Head); err != nil {
		t.Fatal(err)
	}
}

func TestReceiptCannotInventScopeDepthOrSuccessfulExecution(t *testing.T) {
	for _, name := range []string{"missing manifest", "changed scope", "changed base", "downgraded depth", "failed execution"} {
		t.Run(name, func(t *testing.T) {
			r := testResult()
			records := testRecorded()
			original := records[r.ManifestHash]
			switch name {
			case "missing manifest":
				records = nil
			case "changed scope":
				r.ScopeHash = strings.Repeat("e", 64)
			case "changed base":
				r.Base = strings.Repeat("e", 40)
			case "downgraded depth":
				original.Manifest.Policy.Depth = "full"
				records[r.ManifestHash] = original
			case "failed execution":
				original.Terminal.Exit = 42
				original.Terminal.Execution = "failed"
				records[r.ManifestHash] = original
			}
			review := Review{ID: r.Task, Runs: map[int64]string{7: r.Actor}, Recorded: records, Comments: []string{r.Comment()}}
			if err := reviewVerdict(review, r.Head); err == nil {
				t.Fatal("invented review completion accepted")
			}
		})
	}
}

func TestEmbeddedTypedVerdict(t *testing.T) {
	for _, name := range []string{"accepted", "wrong head", "wrong run", "wrong actor", "missing manifest", "missing prompt", "wrong scope", "missing findings", "partial", "not assessed", "minor findings", "duplicate key", "duplicate receipt", "malformed", "legacy contradiction", "invalid legacy token", "earlier malformed", "earlier minor"} {
		t.Run(name, func(t *testing.T) {
			result := testResult()
			records := testRecorded()
			original := records[result.ManifestHash]
			original.Manifest.PromptHash = strings.Repeat("e", 64)
			records[result.ManifestHash] = original
			review := Review{ID: result.Task, Runs: map[int64]string{7: result.Actor}, Recorded: records}
			switch name {
			case "wrong head":
				result.Head = strings.Repeat("f", 40)
			case "wrong run":
				result.RunID++
			case "wrong actor":
				result.Actor = "operator-other"
			case "missing manifest":
				review.Recorded = nil
			case "missing prompt":
				original.Manifest.PromptHash = ""
				records[result.ManifestHash] = original
			case "wrong scope":
				result.ScopeHash = strings.Repeat("f", 64)
			case "partial":
				result.Coverage = "partial"
			case "not assessed":
				result.Source = "not-assessed"
			case "minor findings":
				result.Source = "findings"
				result.Findings = []ReviewFinding{{ID: "minor", Severity: "minor", Summary: "suggestion"}}
			case "legacy contradiction":
				review.Comments = []string{"Head: " + result.Head + "\nNOT CLEAN"}
			case "earlier malformed":
				review.Comments = []string{ResultPrefix + "{broken"}
			case "earlier minor":
				earlier := result
				earlier.Source = "findings"
				earlier.Findings = []ReviewFinding{{ID: "minor", Severity: "minor", Summary: "suggestion"}}
				review.Comments = []string{earlier.Comment()}
			}
			receipt := result.Comment()
			switch name {
			case "missing findings":
				receipt = strings.Replace(receipt, `,"findings":[]`, "", 1)
			case "duplicate key":
				receipt = strings.Replace(receipt, `"source":"clear"`, `"source":"findings","source":"clear"`, 1)
			case "duplicate receipt":
				receipt += "\n" + receipt
			case "malformed":
				receipt += "{"
			case "invalid legacy token":
				receipt = "CLEAN in prose only"
			}
			review.Comments = append(review.Comments, "Head: "+result.Head+"\nOriginal report\n\n"+receipt+"\n\nCE leaf output:\n{\"status\":\"complete\"}")
			err := reviewVerdict(review, testResult().Head)
			if (err == nil) != (name == "accepted") {
				t.Fatalf("verdict: %v", err)
			}
		})
	}
}

func TestEmbeddedReceiptRequiresEveryIdentityAndOutcomeField(t *testing.T) {
	result := testResult()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimPrefix(result.Comment(), ResultPrefix)), &fields); err != nil {
		t.Fatal(err)
	}
	for field := range fields {
		if field == "reason" {
			continue
		}
		t.Run(field, func(t *testing.T) {
			copyFields := map[string]json.RawMessage{}
			for k, v := range fields {
				if k != field {
					copyFields[k] = v
				}
			}
			raw, err := json.Marshal(copyFields)
			if err != nil {
				t.Fatal(err)
			}
			_, present, err := ParseReviewResult("Original report\n" + ResultPrefix + string(raw))
			if !present || err == nil {
				t.Fatalf("missing %s accepted: present=%v err=%v", field, present, err)
			}
		})
	}
	pretty, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	parsed, present, err := ParseReviewResult(ResultPrefix + string(pretty))
	if !present || err != nil || parsed.Task != result.Task {
		t.Fatalf("legacy formatted receipt: %v", err)
	}
}

func TestSplitReceiptForm(t *testing.T) {
	result := testResult()
	obj := strings.TrimPrefix(result.Comment(), ResultPrefix)
	for name, tc := range map[string]struct {
		text string
		ok   bool
	}{
		"accepted":         {"Head: x\n" + ResultMarker + "\n" + obj + "\nCE:", true},
		"standalone":       {ResultMarker + "\n" + obj, true},
		"blank line":       {ResultMarker + "\n\n" + obj, false},
		"wrapped":          {ResultMarker + "\n{\n" + obj[1:], false},
		"prose between":    {ResultMarker + "\nnote\n" + obj, false},
		"marker at end":    {"report\n" + ResultMarker, false},
		"leading space":    {ResultMarker + "\n " + obj, false},
		"duplicate":        {ResultMarker + "\n" + obj + "\n" + ResultPrefix + obj, false},
		"duplicate split":  {ResultMarker + "\n" + obj + "\n" + ResultMarker + "\n" + obj, false},
		"duplicate key":    {ResultMarker + "\n" + strings.Replace(obj, `"source":"clear"`, `"source":"findings","source":"clear"`, 1), false},
		"trailing garbage": {ResultMarker + "\n" + obj + "x", false},
	} {
		t.Run(name, func(t *testing.T) {
			parsed, present, err := ParseReviewResult(tc.text)
			if !present || (err == nil) != tc.ok {
				t.Fatalf("present=%v err=%v", present, err)
			}
			if tc.ok && parsed.Task != result.Task {
				t.Fatal("wrong task")
			}
		})
	}
}

func TestSplitReceiptVerdict(t *testing.T) {
	result := testResult()
	records := testRecorded()
	original := records[result.ManifestHash]
	original.Manifest.PromptHash = strings.Repeat("e", 64)
	records[result.ManifestHash] = original
	obj := strings.TrimPrefix(result.Comment(), ResultPrefix)
	for _, partial := range []bool{false, true} {
		r := result
		if partial {
			r.Coverage = "partial"
		}
		obj = strings.TrimPrefix(r.Comment(), ResultPrefix)
		review := Review{ID: r.Task, Runs: map[int64]string{7: r.Actor}, Recorded: records,
			Comments: []string{"Head: " + r.Head + "\nreport\n" + ResultMarker + "\n" + obj + "\nCE"}}
		if err := reviewVerdict(review, r.Head); (err == nil) == partial {
			t.Fatalf("partial=%v err=%v", partial, err)
		}
	}
}
