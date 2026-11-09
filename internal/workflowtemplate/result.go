package workflowtemplate

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/example-user/commander/internal/beads"
)

const ResultPrefix = "workflow-review-result@v1 "

// ResultMarker is the receipt marker alone on a line (split form).
const ResultMarker = "workflow-review-result@v1"

// ReviewResult separates source findings from an interrupted review execution.
type ReviewResult struct {
	Task         string          `json:"task"`
	RunID        int64           `json:"run_id"`
	Actor        string          `json:"actor"`
	Head         string          `json:"head"`
	Base         string          `json:"base"`
	ScopeHash    string          `json:"scope_hash"`
	ManifestHash string          `json:"manifest_hash"`
	Depth        string          `json:"depth"`
	Execution    string          `json:"execution"`
	Source       string          `json:"source"`
	Coverage     string          `json:"coverage"`
	Reason       string          `json:"reason,omitempty"`
	Findings     []ReviewFinding `json:"findings"`
	// Resolved names, on a delta recheck, each blocker/major finding of the
	// prior review that this review verified fixed in the delta.
	Resolved []string `json:"resolved,omitempty"`
}

type ReviewFinding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
}

// ParseReviewResult recognizes one exact receipt line, including inside an original
// report. Prose and CE JSON are never interpreted as a source verdict.
func ParseReviewResult(comment string) (ReviewResult, bool, error) {
	var result ReviewResult
	var raw string
	found := false
	lines := strings.Split(comment, "\n")
	for i, line := range lines {
		value, ok := strings.CutPrefix(line, ResultPrefix)
		if line == ResultMarker {
			// Split form: the marker alone, then one complete JSON object on the
			// very next line. No blank line, wrapped object or prose is accepted.
			if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "{") || !json.Valid([]byte(lines[i+1])) {
				return result, true, fmt.Errorf("typed review result marker must be followed by one complete JSON object line")
			}
			value, ok = lines[i+1], true
		}
		if ok {
			if found {
				return result, true, fmt.Errorf("ambiguous typed review results in one report")
			}
			found, raw = true, value
		}
	}
	if !found {
		return result, false, nil
	}
	// Preserve standalone receipts whose JSON was formatted across several lines.
	if suffix, ok := strings.CutPrefix(comment, ResultPrefix); ok && json.Valid([]byte(suffix)) {
		raw = suffix
	}
	// encoding/json otherwise accepts duplicate keys with last-value-wins semantics.
	dec := json.NewDecoder(strings.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return result, true, fmt.Errorf("malformed typed review result object")
	}
	fields := map[string]bool{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return result, true, err
		}
		name, ok := key.(string)
		if !ok || fields[name] {
			return result, true, fmt.Errorf("ambiguous typed review result field %v", key)
		}
		switch name {
		case "task", "run_id", "actor", "head", "base", "scope_hash", "manifest_hash", "depth", "execution", "source", "coverage", "findings", "reason", "resolved":
		default:
			return result, true, fmt.Errorf("unknown typed review result field %s", name)
		}
		fields[name] = true
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return result, true, err
		}
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return result, true, fmt.Errorf("malformed typed review result: %w", err)
	}
	for _, name := range []string{"task", "run_id", "actor", "head", "base", "scope_hash", "manifest_hash", "depth", "execution", "source", "coverage", "findings"} {
		if !fields[name] {
			return result, true, fmt.Errorf("malformed typed review result: missing %s", name)
		}
	}
	return result, true, nil
}

func (r ReviewResult) Comment() string {
	b, _ := json.Marshal(r)
	return ResultPrefix + string(b)
}

func hexLength(s string, n int) bool {
	_, err := hex.DecodeString(s)
	return len(s) == n && err == nil
}

func (r ReviewResult) validate(review Review) error {
	actor := beads.OwnerLabelName(strings.ToLower(r.Actor))
	if r.Task != review.ID || r.RunID <= 0 || review.Runs[r.RunID] != actor || !strings.HasPrefix(actor, "operator-") {
		return fmt.Errorf("review result must name an actual run and Operator of task %s", review.ID)
	}
	if !hexLength(r.Head, 40) || !hexLength(r.Base, 40) || !hexLength(r.ScopeHash, 64) || !hexLength(r.ManifestHash, 64) {
		return fmt.Errorf("review result requires full head/base and original scope/manifest SHA256 digests")
	}
	original, ok := review.Recorded[r.ManifestHash]
	m := original.Manifest
	if !ok || m.Version != "review-run@v1" || m.Task != r.Task || m.RunID != r.RunID || beads.OwnerLabelName(strings.ToLower(m.Actor)) != actor || m.Policy.Head != r.Head || m.Policy.Base != r.Base || m.Policy.ScopeHash != r.ScopeHash || (m.Policy.Depth == "full" && r.Depth != "full") {
		return fmt.Errorf("review result does not match original task/run scope, actor or required depth")
	}
	if r.Execution == "complete" && (original.Terminal.Execution != "complete" || original.Terminal.Exit != 0) {
		return fmt.Errorf("successful review execution requires an actual successful terminal result")
	}
	if r.Depth != "focused" && r.Depth != "full" {
		return fmt.Errorf("review result depth must be focused or full")
	}
	if r.Execution != "complete" && r.Execution != "blocked" && r.Execution != "failed" {
		return fmt.Errorf("unknown review execution %q", r.Execution)
	}
	if r.Source != "clear" && r.Source != "findings" && r.Source != "not-assessed" {
		return fmt.Errorf("unknown review source outcome %q", r.Source)
	}
	if r.Coverage != "complete" && r.Coverage != "partial" {
		return fmt.Errorf("unknown review coverage %q", r.Coverage)
	}
	if r.Execution != "complete" && (r.Reason == "" || r.Coverage == "complete") {
		return fmt.Errorf("blocked/failed review needs a reason and partial coverage")
	}
	if r.Source == "not-assessed" && r.Coverage == "complete" {
		return fmt.Errorf("unassessed source cannot have complete coverage")
	}
	if (r.Source == "findings") != (len(r.Findings) > 0) {
		return fmt.Errorf("source outcome and findings disagree")
	}
	seen := map[string]bool{}
	for _, f := range r.Findings {
		if f.ID == "" || seen[f.ID] || f.Summary == "" || (f.Severity != "blocker" && f.Severity != "major" && f.Severity != "minor") {
			return fmt.Errorf("review findings require unique IDs, severity and summary")
		}
		seen[f.ID] = true
	}
	resolved := map[string]bool{}
	for _, id := range r.Resolved {
		if id == "" || resolved[id] {
			return fmt.Errorf("resolved prior findings need unique non-empty IDs")
		}
		resolved[id] = true
	}
	return nil
}

// resultVerdict preserves actual findings while allowing missing coverage to finish.
func resultVerdict(r Review, head string) (bool, error) {
	return resultVerdictRetaining(r, head, "")
}

// resultVerdictRetaining is resultVerdict with one exact report the caller has
// already accounted for, so its NOT CLEAN is not read as ambiguous. Findings and
// coverage are judged as ever.
func resultVerdictRetaining(r Review, head, retained string) (bool, error) {
	var latest *ReviewResult
	var latestErr error
	sawResult := false
	embedded := false
	unsafeEmbedded := false
	for _, comment := range r.Comments {
		result, ok, parseErr := ParseReviewResult(comment)
		if !ok {
			continue
		}
		sawResult = true
		if parseErr != nil {
			latestErr = parseErr
			unsafeEmbedded = true
			continue
		}
		suffix, standalone := strings.CutPrefix(comment, ResultPrefix)
		embedded = !standalone || !json.Valid([]byte(suffix))
		if result.Head == head && (result.Source != "clear" || len(result.Findings) != 0 || result.Execution != "complete" || result.Coverage != "complete") {
			unsafeEmbedded = true
		}
		if embedded {
			original := r.Recorded[result.ManifestHash]
			if result.Execution != "complete" || result.Source != "clear" || result.Coverage != "complete" || (result.Findings == nil || len(result.Findings) != 0) || !hexLength(original.Manifest.PromptHash, 64) {
				return true, fmt.Errorf("embedded typed verdict requires clear complete source without findings and original prompt provenance")
			}
		}
		if result.Head == head && blockingFindings(result.Findings) {
			return true, fmt.Errorf("review %s has source findings on head %s; resolve them and review the changed scope", r.ID, head)
		}
		if err := result.validate(r); err != nil {
			latestErr = err
			unsafeEmbedded = true
			continue
		}
		latestErr = nil
		latest = &result
	}
	if latestErr != nil {
		return true, latestErr
	}
	if latest == nil {
		return sawResult, fmt.Errorf("review has no valid typed result")
	}
	if embedded && unsafeEmbedded {
		return true, fmt.Errorf("ambiguous or incomplete original typed review history")
	}
	if latest.Head != head {
		return true, fmt.Errorf("review result does not identify current PR head %s", head)
	}
	// Legacy NOT CLEAN did not distinguish bugs from unavailable coverage. Never
	// silently reinterpret that historical record as an execution-only failure.
	for _, comment := range r.Comments {
		if comment == retained {
			continue
		}
		if _, bad := verdictLines(comment); bad && strings.Contains(comment, head) {
			return true, fmt.Errorf("legacy NOT CLEAN on head %s is ambiguous; obtain a separate truthful review result, without rewriting history", head)
		}
	}
	if latest.Execution != "complete" || latest.Coverage != "complete" || latest.Source == "not-assessed" {
		return true, fmt.Errorf("review execution=%s source=%s coverage=%s: %s; resume only missing coverage, do not fix unchanged code or restart the roster", latest.Execution, latest.Source, latest.Coverage, latest.Reason)
	}
	return true, nil
}

func blockingFindings(findings []ReviewFinding) bool {
	for _, finding := range findings {
		if finding.Severity != "minor" {
			return true
		}
	}
	return false
}
