package workflowtemplate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// readEvidence rejects links and bounds reads. This is a local integrity check,
// not protection against hostile same-UID concurrent filesystem mutation.
func readEvidence(path string) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("evidence path must be absolute and clean")
	}
	for p := path; ; p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("linked evidence path")
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > 16<<20 {
		return nil, fmt.Errorf("evidence must be a regular file of at most 16 MiB")
	}
	return os.ReadFile(path)
}

// VerifyAddendumArtifacts reads only the assessor-anchored original inventory
// and transcript. It never executes artifact content or searches other runs.
func VerifyAddendumArtifacts(a AddendumAssessment, originalText string) error {
	if filepath.Base(a.ManifestPath) != "final-manifest.json" || !strings.Contains(originalText, a.ManifestSHA256) || !strings.Contains(originalText, filepath.Dir(a.ManifestPath)) {
		return fmt.Errorf("original report does not anchor manifest path and digest")
	}
	raw, err := readEvidence(a.ManifestPath)
	if err != nil {
		return err
	}
	if TextDigest(string(raw)) != a.ManifestSHA256 {
		return fmt.Errorf("manifest digest differs")
	}
	var m struct {
		Schema string            `json:"schema"`
		Task   string            `json:"task"`
		RunID  string            `json:"run_id"`
		Head   string            `json:"head"`
		Base   string            `json:"base"`
		Files  map[string]string `json:"files"`
	}
	if err = json.Unmarshal(raw, &m); err != nil {
		return err
	}
	if m.Schema != "review-artifact-manifest@v1" || m.Task != a.Original.Bead || m.RunID != a.RunID || m.Head != a.Head || m.Base != a.Base || len(m.Files) == 0 || len(m.Files) > 256 {
		return fmt.Errorf("manifest identity or inventory differs")
	}
	root := filepath.Dir(a.ManifestPath)
	artifacts := map[string][]byte{}
	for name, digest := range m.Files {
		if filepath.IsAbs(name) || filepath.Clean(name) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("unsafe manifest member")
		}
		b, e := readEvidence(filepath.Join(root, name))
		if e != nil {
			return e
		}
		if TextDigest(string(b)) != digest {
			return fmt.Errorf("artifact %s digest differs", name)
		}
		artifacts[name] = b
	}
	if m.Files["original-binding.json"] != a.OriginalBindingSHA256 {
		return fmt.Errorf("original binding digest differs")
	}
	var binding struct {
		Task  string `json:"task"`
		RunID string `json:"run_id"`
		Head  string `json:"head"`
		Base  string `json:"base"`
		Skill string `json:"skill_digest"`
	}
	if json.Unmarshal(artifacts["original-binding.json"], &binding) != nil || binding.Task != a.Original.Bead || binding.RunID != a.RunID || binding.Head != a.Head || binding.Base != a.Base || binding.Skill != a.SkillSHA256 {
		return fmt.Errorf("original binding identity differs")
	}
	var review struct {
		Status     string            `json:"status"`
		Verdict    string            `json:"verdict"`
		Findings   []json.RawMessage `json:"findings"`
		Actionable []json.RawMessage `json:"actionable_findings"`
		Gaps       []json.RawMessage `json:"testing_gaps"`
		Coverage   struct {
			Depth     string `json:"depth"`
			Validator struct {
				Outcome    string `json:"outcome"`
				Confirmed  *int   `json:"confirmed"`
				Rejected   *int   `json:"rejected"`
				Unresolved *int   `json:"unresolved"`
				Malformed  *int   `json:"malformed"`
				Failed     *int   `json:"failed"`
				Skipped    *int   `json:"shortcut_skipped"`
			} `json:"validator"`
		} `json:"coverage"`
	}
	if json.Unmarshal(artifacts["review.json"], &review) != nil || review.Status != "complete" || review.Verdict != "Ready to merge" || review.Findings == nil || len(review.Findings) > 0 || len(review.Actionable) > 0 || len(review.Gaps) > 0 {
		return fmt.Errorf("source review has findings, gaps or incomplete result")
	}
	if review.Coverage.Depth != "full" || review.Coverage.Validator.Outcome != "empty-batch" {
		return fmt.Errorf("mandatory full-depth/empty-validator coverage missing")
	}
	for _, count := range []*int{review.Coverage.Validator.Confirmed, review.Coverage.Validator.Rejected, review.Coverage.Validator.Unresolved, review.Coverage.Validator.Malformed, review.Coverage.Validator.Failed, review.Coverage.Validator.Skipped} {
		if count == nil || *count != 0 {
			return fmt.Errorf("mandatory validator has missing, failed or unresolved results")
		}
	}
	// This enumerated exception supports only the complete original full-depth
	// local fallback packet, including actual launch/terminal/thread evidence.
	earliest := time.Time{}
	threads := map[string]bool{}
	for _, leaf := range []string{"correctness", "project-standards", "testing", "reliability", "learnings", "adversarial", "merge", "report"} {
		var b struct {
			Task   string `json:"task"`
			RunID  string `json:"run_id"`
			Role   string `json:"role"`
			Head   string `json:"head"`
			Base   string `json:"base"`
			Sealed string `json:"sealed_at"`
			Prompt string `json:"prompt_sha256"`
		}
		var launch struct {
			PID     int    `json:"pid"`
			At      string `json:"launch_at"`
			Binding string `json:"binding_sha256"`
		}
		var terminal struct {
			PID    int    `json:"pid"`
			Exit   *int   `json:"exit_code"`
			Status string `json:"status"`
			Exists bool   `json:"artifact_exists"`
		}
		if json.Unmarshal(artifacts[leaf+".binding.json"], &b) != nil || json.Unmarshal(artifacts[leaf+".launch.json"], &launch) != nil || json.Unmarshal(artifacts[leaf+".terminal.json"], &terminal) != nil {
			return fmt.Errorf("missing mandatory %s packet", leaf)
		}
		sealed, e := time.Parse(time.RFC3339Nano, b.Sealed)
		if e != nil {
			return e
		}
		at, e := time.Parse(time.RFC3339Nano, launch.At)
		if e != nil {
			return e
		}
		if len(artifacts[leaf+".return.txt"]) == 0 {
			return fmt.Errorf("missing mandatory %s return", leaf)
		}
		if b.Task != a.Original.Bead || b.RunID != a.RunID || b.Head != a.Head || b.Base != a.Base || b.Role != leaf || b.Prompt != m.Files[leaf+".prompt.txt"] || len(artifacts[leaf+".prompt.txt"]) == 0 || launch.Binding != m.Files[leaf+".binding.json"] || sealed.After(at) || launch.PID <= 0 || terminal.PID != launch.PID || terminal.Exit == nil || *terminal.Exit != 0 || terminal.Status != "terminal" || !terminal.Exists {
			return fmt.Errorf("invalid mandatory %s launch/binding/terminal", leaf)
		}
		if earliest.IsZero() || at.Before(earliest) {
			earliest = at
		}
		thread := ""
		for _, line := range strings.Split(string(artifacts[leaf+".events.jsonl"]), "\n") {
			var event struct {
				Type   string `json:"type"`
				Thread string `json:"thread_id"`
			}
			if json.Unmarshal([]byte(line), &event) == nil && event.Type == "thread.started" {
				if thread != "" {
					return fmt.Errorf("ambiguous leaf thread")
				}
				thread = event.Thread
			}
		}
		if thread == "" || threads[thread] {
			return fmt.Errorf("missing or reused leaf thread")
		}
		threads[thread] = true
	}
	transcript, err := readEvidence(a.TranscriptPath)
	if err != nil {
		return err
	}
	if TextDigest(string(transcript)) != a.TranscriptSHA256 || !strings.Contains(string(transcript), a.PeerJobID) {
		return fmt.Errorf("transcript digest or peer receipt differs")
	}
	receipt := false
	for _, line := range strings.Split(string(transcript), "\n") {
		if !strings.Contains(line, a.PeerJobID) || !strings.Contains(line, strconv.FormatInt(a.PeerStartEpoch, 10)) {
			continue
		}
		var event struct {
			Timestamp string `json:"timestamp"`
		}
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		at, e := time.Parse(time.RFC3339Nano, event.Timestamp)
		if e != nil {
			continue
		}
		start := time.Unix(a.PeerStartEpoch, 0)
		if !at.Before(start) && at.Before(start.Add(time.Minute)) && at.Before(earliest) {
			receipt = true
		}
	}
	if !receipt {
		return fmt.Errorf("contemporaneous peer receipt before local dispatch missing")
	}
	return nil
}
