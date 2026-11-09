package workflowtemplate

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/reviewrun"
)

// RecordReader reads a review's sealed launch record (reviewrun.ReadRecorded
// on Commander home's review-executions).
type RecordReader func(run int64, manifest string) (reviewrun.Recorded, error)

// CheckReport judges a review report and its typed receipt before the reviewer
// posts them and exits, by the rules the close door applies later, so no review
// is lost to format: a "Head: <full sha>" line naming head, a valid
// workflow-review-result@v1 receipt for that head whose sealed launch record
// matches it, and a verdict line that agrees with the receipt (CLEAN with no
// blocker/major, NOT CLEAN with one, neither when execution or coverage is
// incomplete). receipt is the separate receipt comment, "" when it sits inside
// report. It returns the verdict the report carries.
func CheckReport(report, receipt, head string, read RecordReader) (string, error) {
	if !hexLength(head, 40) {
		return "", fmt.Errorf("--head must be the full 40-character sha you reviewed")
	}
	if !hasHeadLine(report, head) {
		return "", fmt.Errorf("the report has no line `Head: %s`: add it (the close door reads only that line)", head)
	}
	text, embedded := receipt, false
	if strings.TrimSpace(receipt) == "" {
		text, embedded = report, true
	}
	result, ok, err := ParseReviewResult(text)
	if !ok {
		return "", fmt.Errorf("no %s receipt found: post it as its own comment (one line: %s{...})", ResultMarker, ResultPrefix)
	}
	if err != nil {
		return "", fmt.Errorf("the receipt does not parse: %w", err)
	}
	if !embedded {
		if suffix, standalone := strings.CutPrefix(text, ResultPrefix); !standalone || !json.Valid([]byte(strings.TrimSpace(suffix))) {
			return "", fmt.Errorf("the receipt comment must be only %s followed by its JSON", ResultPrefix)
		}
	}
	if result.Head != head {
		return "", fmt.Errorf("the receipt names head %s, not the reviewed head %s", result.Head, head)
	}
	if read == nil {
		return "", fmt.Errorf("launch record reader unavailable")
	}
	rec, err := read(result.RunID, result.ManifestHash)
	if err != nil {
		return "", fmt.Errorf("no sealed launch record for run %d manifest %s (run the review under cmdr workflow review-exec and copy its manifest_hash): %w", result.RunID, result.ManifestHash, err)
	}
	actor := beads.OwnerLabelName(strings.ToLower(result.Actor))
	r := Review{ID: result.Task, Runs: map[int64]string{result.RunID: actor}, Recorded: map[string]reviewrun.Recorded{result.ManifestHash: rec}}
	if err := result.validate(r); err != nil {
		return "", fmt.Errorf("the receipt would be refused: %w", err)
	}
	complete := result.Execution == "complete" && result.Coverage == "complete" && result.Source != "not-assessed"
	if embedded && (!complete || result.Source != "clear" || result.Findings == nil || !hexLength(rec.Manifest.PromptHash, 64)) {
		return "", fmt.Errorf("a receipt inside the report is accepted only for a clear complete review with no findings: post it as its own comment")
	}
	clean, notClean := verdictLines(report)
	blocking := blockingFindings(result.Findings)
	switch {
	case !complete && (clean || notClean):
		return "", fmt.Errorf("execution=%s coverage=%s claims no verdict: remove the CLEAN/NOT CLEAN line and report the missing coverage", result.Execution, result.Coverage)
	case !complete:
		return "incomplete", nil
	case clean && notClean:
		return "", fmt.Errorf("the report has both CLEAN and NOT CLEAN lines")
	case blocking && !notClean:
		return "", fmt.Errorf("the receipt has blocker/major findings: end the report with a line NOT CLEAN")
	case !blocking && !clean:
		return "", fmt.Errorf("no blocker/major findings (minors never make NOT CLEAN): end the report with a line CLEAN")
	case blocking:
		return "NOT CLEAN", nil
	}
	return "CLEAN", nil
}
