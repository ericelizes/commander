package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/reviewpolicy"
	"github.com/example-user/commander/internal/reviewrun"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/workflowtemplate"
)

// Keep retained source metadata synthetic and independently mutable without
// adding mutation facilities to the production in-memory client.
type addendumSourceClient struct {
	*beads.MemClient
	changedBead, changedField string
	typedResults              []string // extra review-task comments, removable between cases
}

func (m *addendumSourceClient) Comments(ctx context.Context, dir, id string) ([]beads.Comment, error) {
	cs, err := m.MemClient.Comments(ctx, dir, id)
	if (id == "review-task" || id == "assessor-task") && len(cs) > 0 {
		cs[0].Author = "synthetic-" + id
		cs[0].CreatedAt = "2026-01-02T03:04:05Z"
		if id == m.changedBead {
			if m.changedField == "author" {
				cs[0].Author = "synthetic-other"
			} else {
				cs[0].CreatedAt = "2026-01-03T03:04:05Z"
			}
		}
	}
	if id == "review-task" {
		for i, text := range m.typedResults {
			cs = append(cs[:len(cs):len(cs)], beads.Comment{ID: fmt.Sprintf("typed-%d", i), Text: text})
		}
	}
	return cs, err
}

const typedAddendumRun, typedAddendumActor = 7001, "operator-synthetic-review"

// typedAddendumResult is a valid workflow-review-result@v1 comment on the seeded
// original run: its launch record sits in the home the door reads, so only what
// the result says can refuse it.
func typedAddendumResult(t *testing.T, a workflowtemplate.AddendumAssessment, edit func(*workflowtemplate.ReviewResult)) string {
	t.Helper()
	scope := strings.Repeat("c", 64)
	manifest, err := json.Marshal(reviewrun.Manifest{Version: "review-run@v1", Task: a.Original.Bead, RunID: typedAddendumRun, Actor: typedAddendumActor, PromptHash: strings.Repeat("e", 64), Policy: reviewpolicy.Result{Base: a.Base, Head: a.Head, ScopeHash: scope, Depth: "full"}})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(manifest)
	hash := hex.EncodeToString(sum[:])
	started := time.Unix(100, 0).UTC()
	terminal, err := json.Marshal(reviewrun.Result{ManifestHash: hash, PID: 1, Started: started, Finished: started, Execution: "complete"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(agentrt.CommanderHome(), "review-executions", strconv.Itoa(typedAddendumRun))
	if err = os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, hash+".json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, hash+".terminal.json"), terminal, 0o600); err != nil {
		t.Fatal(err)
	}
	r := workflowtemplate.ReviewResult{Task: a.Original.Bead, RunID: typedAddendumRun, Actor: typedAddendumActor, Head: a.Head, Base: a.Base, ScopeHash: scope, ManifestHash: hash, Depth: "full", Execution: "complete", Source: "clear", Coverage: "complete", Findings: []workflowtemplate.ReviewFinding{}}
	if edit != nil {
		edit(&r)
	}
	return r.Comment()
}

func seedAddendumArtifacts(t *testing.T) (workflowtemplate.AddendumAssessment, string) {
	t.Helper()
	root := t.TempDir()
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	digest := workflowtemplate.TextDigest
	a := workflowtemplate.AddendumAssessment{Original: workflowtemplate.CommentRef{Bead: "review-task", ID: "comment-1"}, Head: head, Base: base, RunID: "synthetic-run", ManifestPath: filepath.Join(root, "final-manifest.json"), TranscriptPath: filepath.Join(root, "transcript.jsonl"), SkillSHA256: digest("skill"), PeerRuleSHA256: digest("peer rule"), RecoveryRuleSHA256: digest("recovery rule"), PeerJobID: "synthetic-peer-job", PeerStartEpoch: 100, Rule: workflowtemplate.WithdrawnPeerSeal, SoleObjection: true, NoFindings: true, MandatoryCoverageComplete: true, ValidatorsComplete: true, PeerProducedNoReview: true, QuotaFallbackAuthorized: true, StartPersistedBeforeLocal: true}
	write := func(name string, v any) string {
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(root, name), b, 0600); e != nil {
			t.Fatal(e)
		}
		return digest(string(b))
	}
	binding := map[string]any{"task": a.Original.Bead, "run_id": a.RunID, "head": head, "base": base, "skill_digest": a.SkillSHA256}
	a.OriginalBindingSHA256 = write("original-binding.json", binding)
	reviewDigest := write("review.json", map[string]any{"status": "complete", "verdict": "Ready to merge", "findings": []string{}, "actionable_findings": []string{}, "testing_gaps": []string{}, "coverage": map[string]any{"depth": "full", "validator": map[string]any{"outcome": "empty-batch", "confirmed": 0, "rejected": 0, "unresolved": 0, "malformed": 0, "failed": 0, "shortcut_skipped": 0}}})
	files := map[string]string{"original-binding.json": a.OriginalBindingSHA256, "review.json": reviewDigest}
	for i, leaf := range []string{"correctness", "project-standards", "testing", "reliability", "learnings", "adversarial", "merge", "report"} {
		name := leaf + ".prompt.txt"
		if err := os.WriteFile(filepath.Join(root, name), []byte("synthetic prompt"), 0600); err != nil {
			t.Fatal(err)
		}
		files[name] = digest("synthetic prompt")
		files[leaf+".binding.json"] = write(leaf+".binding.json", map[string]any{"task": a.Original.Bead, "run_id": a.RunID, "role": leaf, "head": head, "base": base, "sealed_at": "1970-01-01T00:03:19Z", "prompt_sha256": files[name]})
		files[leaf+".launch.json"] = write(leaf+".launch.json", map[string]any{"pid": i + 1, "launch_at": "1970-01-01T00:03:20Z", "binding_sha256": files[leaf+".binding.json"]})
		files[leaf+".terminal.json"] = write(leaf+".terminal.json", map[string]any{"pid": i + 1, "exit_code": 0, "status": "terminal", "artifact_exists": true})
		returnName := leaf + ".return.txt"
		if err := os.WriteFile(filepath.Join(root, returnName), []byte("synthetic completed review"), 0600); err != nil {
			t.Fatal(err)
		}
		files[returnName] = digest("synthetic completed review")
		files[leaf+".events.jsonl"] = write(leaf+".events.jsonl", map[string]any{"type": "thread.started", "thread_id": leaf + "-thread"})
	}
	a.ManifestSHA256 = write("final-manifest.json", map[string]any{"schema": "review-artifact-manifest@v1", "task": a.Original.Bead, "run_id": a.RunID, "head": head, "base": base, "files": files})
	transcript := `{"timestamp":"1970-01-01T00:01:40Z","receipt":"synthetic-peer-job epoch 100"}`
	if err := os.WriteFile(a.TranscriptPath, []byte(transcript), 0600); err != nil {
		t.Fatal(err)
	}
	a.TranscriptSHA256 = digest(transcript)
	original := "Head: " + head + "\nOriginal manifest " + root + " " + a.ManifestSHA256 + "\nExtra failed-peer seal policy objection only.\nNOT CLEAN"
	a.Original.SHA256 = digest(original)
	return a, original
}

func TestWorkflowAddendumOrdinaryClose(t *testing.T) {
	for _, prose := range []bool{false, true} {
		t.Run(fmt.Sprintf("prose-%v", prose), func(t *testing.T) { testWorkflowAddendumOrdinaryClose(t, prose) })
	}
}

func testWorkflowAddendumOrdinaryClose(t *testing.T, prose bool) {
	rawScenario, e := os.ReadFile("../../tests/evals/scenarios/review-addendum.json")
	if e != nil {
		t.Fatal(e)
	}
	var scenario struct {
		Refusals              []string `json:"refusals"`
		TranscriptionRefusals []string `json:"transcription_refusals"`
	}
	if e = json.Unmarshal(rawScenario, &scenario); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	mem := &addendumSourceClient{MemClient: &beads.MemClient{}}
	setupObjectiveProject(t, mem)
	res := seedReviewWorkflow(t, mem.MemClient)
	a, original := seedAddendumArtifacts(t)
	head := a.Head
	pinWorkflowGH(t, func(context.Context, ...string) ([]byte, error) {
		return []byte(`{"headRefOid":"` + head + `","baseRefOid":"` + a.Base + `","state":"OPEN","commits":[],"statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"}]}`), nil
	})
	// Replace the synthetic binding and provision the local registered Manager.
	binding := workflowtemplate.WriterBinding{Revision: "current", PR: testPR, Head: head, Tasks: []string{"writer-task"}}
	mem.AddComment(ctx, "/d", res.Container, binding.Comment())
	st, done, err := needsYouStoreFn()
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	p, err := st.GetProjectByName(ctx, "manifest")
	if err != nil || p == nil {
		t.Fatalf("project: %v %v", p, err)
	}
	if err = st.UpsertWorkstreamManager(ctx, store.WorkstreamManager{ProjectID: p.ID, WorkstreamBeadID: "B", SessionFile: "/fixture/session"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"review-task", "assessor-task", "policy-task"} {
		mem.Seed(beads.Bead{ID: id, Status: "closed"})
		mem.SetParent(ctx, "/d", id, "B")
	}
	mem.AddComment(ctx, "/d", "review-task", original)
	raw, _ := json.Marshal(a)
	assessmentText := workflowtemplate.AssessmentPrefix + string(raw)
	if prose {
		assessmentText = "Retained evidence assessment only. Original reviewers retain authorship and NOT CLEAN. All mandatory local coverage and validators complete; peer produced no review due quota; persisted start preceded local dispatch. Extra failed-peer seal is Manager policy, not a Skill requirement."
	}
	mem.AddComment(ctx, "/d", "assessor-task", assessmentText)
	mem.AddComment(ctx, "/d", "policy-task", "Extra dedicated prelaunch failed-peer seal policy to withdraw.")
	step := res.Remaining[1].BeadID
	mem.Close(ctx, "/d", res.Remaining[0].BeadID)
	proof := workflowtemplate.Proof{ReviewBead: "review-task", Head: head, NoFindings: true}
	postProof := func() {
		raw, _ := json.Marshal(proof)
		mem.AddComment(ctx, "/d", step, workflowtemplate.ProofPrefix+string(raw))
	}
	postProof()
	if err = checkWorkflowClose(ctx, mem, "/d", "manifest", step); err == nil {
		t.Fatal("ordinary close accepted NOT CLEAN without addendum")
	}
	args := []string{"--project", "manifest", "--block", res.Container, "--step", step, "--original-comment", "comment-1", "--assessment-bead", "assessor-task", "--assessment-comment", "comment-1", "--policy-bead", "policy-task", "--policy-comment", "comment-1"}
	if prose {
		path := filepath.Join(t.TempDir(), "transcription.json")
		transcription := workflowtemplate.AddendumTranscription{Source: workflowtemplate.CommentRef{Bead: "assessor-task", ID: "comment-1", SHA256: workflowtemplate.TextDigest(assessmentText)}, Claims: a}
		write := func(v any) {
			t.Helper()
			b, _ := json.Marshal(v)
			if err := os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("COMMANDER_ROLE", "manager-B")
		out, errout, read := withTempOutputFiles(t)
		if runWorkflowAddendum(args, out, errout) == 0 {
			t.Fatal("prose silently accepted without explicit claims")
		}
		read()
		args = append(args, "--evidence-file", path)
		t.Setenv("COMMANDER_ROLE", "manager-B")
		for _, name := range scenario.TranscriptionRefusals {
			t.Run(name, func(t *testing.T) {
				bad := transcription
				switch name {
				case "wrong-source-id":
					bad.Source.ID = "other"
				case "wrong-source-digest":
					bad.Source.SHA256 = workflowtemplate.TextDigest("other")
				case "unsupported-claim":
					bad.Claims.Rule = "general-override"
				}
				if name == "author-impersonation" {
					write(map[string]any{"source": bad.Source, "claims": bad.Claims, "assessment_author": "operator-original"})
				} else {
					write(bad)
				}
				before, _ := mem.Comments(ctx, "/d", res.Container)
				out, errout, read := withTempOutputFiles(t)
				if runWorkflowAddendum(args, out, errout) == 0 {
					t.Fatal("invalid transcription recorded")
				}
				read()
				after, _ := mem.Comments(ctx, "/d", res.Container)
				if len(before) != len(after) {
					t.Fatal("refusal wrote record")
				}
			})
		}
		write(transcription)
		t.Setenv("COMMANDER_ROLE", "manager-foreign")
		out, errout, read = withTempOutputFiles(t)
		if runWorkflowAddendum(args, out, errout) == 0 {
			t.Fatal("foreign Manager recorded")
		}
		read()
	}
	t.Setenv("COMMANDER_ROLE", "operator-synthetic")
	out, errout, read := withTempOutputFiles(t)
	if runWorkflowAddendum(args, out, errout) == 0 {
		t.Fatal("operator recorded addendum")
	}
	read()
	out, errout, read = withTempOutputFiles(t)
	if runInternalComment([]string{"--project", "manifest", "--bead", res.Container, "--text", workflowtemplate.AddendumPrefix + "{}"}, nil, out, errout) == 0 {
		t.Fatal("generic comment forged record")
	}
	read()
	t.Setenv("COMMANDER_ROLE", "manager-B")
	out, errout, read = withTempOutputFiles(t)
	if code := runWorkflowAddendum(args, out, errout); code != 0 {
		_, e := read()
		t.Fatalf("record code=%d %s", code, e)
	}
	text, _ := read()
	var ref workflowtemplate.AddendumRef
	if err = json.Unmarshal([]byte(text), &ref); err != nil {
		t.Fatal(err)
	}
	proof.Addendum = &ref
	postProof()
	if err = checkWorkflowClose(ctx, mem, "/d", "manifest", step); err != nil {
		t.Fatalf("ordinary close: %v", err)
	}
	comments, _ := mem.Comments(ctx, "/d", "review-task")
	if len(comments) != 1 || comments[0].Text != original {
		t.Fatal("original history changed")
	}
	evidence, _ := mem.Comments(ctx, "/d", step)
	if !strings.Contains(evidence[len(evidence)-1].Text, "original NOT CLEAN retained") {
		t.Fatal("missing explicit conformance attribution")
	}
	{
		records, _ := mem.Comments(ctx, "/d", res.Container)
		var record workflowtemplate.AddendumRecord
		for _, c := range records {
			if strings.HasPrefix(c.Text, workflowtemplate.AddendumPrefix) {
				json.Unmarshal([]byte(strings.TrimPrefix(c.Text, workflowtemplate.AddendumPrefix)), &record)
			}
		}
		if (prose && record.Representation != workflowtemplate.ManagerTranscription) || record.Actor != "manager-b" && record.Actor != "manager-B" || record.RecordedAt == "" || len(record.AssessorRoles) == 0 {
			t.Fatalf("missing attribution: %+v", record)
		}
		if record.OriginalAuthor != "synthetic-review-task" || record.AssessmentAuthor != "synthetic-assessor-task" || record.OriginalCreatedAt != "2026-01-02T03:04:05Z" || record.AssessmentCreatedAt != "2026-01-02T03:04:05Z" {
			t.Fatalf("source attribution not retained: %+v", record)
		}
		source, _ := mem.Comments(ctx, "/d", "assessor-task")
		if len(source) != 1 || source[0].Text != assessmentText {
			t.Fatal("assessor history changed")
		}
	}
	// Same request must not append another record.
	before, _ := mem.Comments(ctx, "/d", res.Container)
	out, errout, read = withTempOutputFiles(t)
	if code := runWorkflowAddendum(args, out, errout); code != 0 {
		_, e := read()
		t.Fatal(e)
	}
	read()
	after, _ := mem.Comments(ctx, "/d", res.Container)
	if len(before) != len(after) {
		t.Fatal("idempotent request appended")
	}
	// A complete, clear typed result does not disturb the retained report. A typed
	// finding on the head refuses at the record door too, and writes nothing.
	previousRuns := workflowResultRuns
	workflowResultRuns = func(context.Context, string, string) (map[int64]string, error) {
		return map[int64]string{typedAddendumRun: typedAddendumActor}, nil
	}
	defer func() { workflowResultRuns = previousRuns }()
	mem.typedResults = []string{typedAddendumResult(t, a, nil)}
	if err = checkWorkflowClose(ctx, mem, "/d", "manifest", step); err != nil {
		t.Fatalf("typed clear result with retained NOT CLEAN: %v", err)
	}
	out, errout, read = withTempOutputFiles(t)
	if code := runWorkflowAddendum(args, out, errout); code != 0 {
		_, e := read()
		t.Fatalf("record with typed clear result: code=%d %s", code, e)
	}
	read()
	mem.typedResults = []string{typedAddendumResult(t, a, func(r *workflowtemplate.ReviewResult) {
		r.Source = "findings"
		r.Findings = []workflowtemplate.ReviewFinding{{ID: "finding-1", Severity: "major", Summary: "synthetic source defect"}}
	})}
	before, _ = mem.Comments(ctx, "/d", res.Container)
	out, errout, read = withTempOutputFiles(t)
	code := runWorkflowAddendum(args, out, errout)
	_, recordErr := read()
	if code == 0 || !strings.Contains(recordErr, "source findings") {
		t.Errorf("record with typed finding: code=%d %s", code, recordErr)
	}
	after, _ = mem.Comments(ctx, "/d", res.Container)
	if len(before) != len(after) {
		t.Fatal("refused record wrote a comment")
	}
	mem.typedResults = nil
	t.Log("original verdict: NOT CLEAN (unchanged); original review: review-task")
	t.Log("ordinary close: conformance addendum accepted; original NOT CLEAN retained")
	for _, name := range scenario.Refusals {

		t.Run(name, func(t *testing.T) {
			previousRoles := workflowTaskRoles
			defer func() { workflowTaskRoles = previousRoles }()
			previousGH := workflowGH
			defer func() { workflowGH = previousGH }()
			savedProof := proof
			defer func() { proof = savedProof; postProof() }()
			wantRefusal := ""
			switch name {
			case "original-author", "original-time", "assessment-author", "assessment-time":
				mem.changedBead = "review-task"
				if strings.HasPrefix(name, "assessment-") {
					mem.changedBead = "assessor-task"
				}
				mem.changedField = "time"
				if strings.HasSuffix(name, "-author") {
					mem.changedField = "author"
				}
				defer func() { mem.changedBead, mem.changedField = "", "" }()
			case "stale-head":
				workflowGH = func(context.Context, ...string) ([]byte, error) {
					return []byte(`{"headRefOid":"changed","baseRefOid":"` + a.Base + `","state":"OPEN"}`), nil
				}
			case "stale-base":
				workflowGH = func(context.Context, ...string) ([]byte, error) {
					return []byte(`{"headRefOid":"` + head + `","baseRefOid":"changed","state":"OPEN"}`), nil
				}
			case "stale-binding":
				changed := binding
				changed.Revision = "changed"
				mem.AddComment(ctx, "/d", res.Container, changed.Comment())
				defer mem.AddComment(ctx, "/d", res.Container, binding.Comment())
			case "wrong-digest":
				r := *proof.Addendum
				r.SHA256 = workflowtemplate.TextDigest("wrong")
				proof.Addendum = &r
				postProof()
			case "unknown-record":
				r := *proof.Addendum
				r.ID = "unknown"
				proof.Addendum = &r
				postProof()
			case "assessor-overlaps-writer", "assessor-overlaps-original", "missing-run":
				workflowTaskRoles = func(ctx context.Context, p, task string) ([]string, error) {
					if task == "assessor-task" {
						if name == "missing-run" {
							return nil, fmt.Errorf("missing run")
						}
						other := "writer-task"
						if name == "assessor-overlaps-original" {
							other = "review-task"
						}
						return previousRoles(ctx, p, other)
					}
					return previousRoles(ctx, p, task)
				}
			case "transcript-tamper":
				saved, e := os.ReadFile(a.TranscriptPath)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(a.TranscriptPath, []byte("changed"), 0600); e != nil {
					t.Fatal(e)
				}
				defer os.WriteFile(a.TranscriptPath, saved, 0600)
			case "typed-major-finding":
				wantRefusal = "source findings"
				mem.typedResults = []string{typedAddendumResult(t, a, func(r *workflowtemplate.ReviewResult) {
					r.Source = "findings"
					r.Findings = []workflowtemplate.ReviewFinding{{ID: "finding-1", Severity: "major", Summary: "synthetic source defect"}}
				})}
				defer func() { mem.typedResults = nil }()
			case "typed-execution-blocked":
				wantRefusal = "execution=blocked"
				mem.typedResults = []string{typedAddendumResult(t, a, func(r *workflowtemplate.ReviewResult) {
					r.Execution, r.Source, r.Coverage, r.Reason = "blocked", "not-assessed", "partial", "synthetic provider quota"
				})}
				defer func() { mem.typedResults = nil }()
			case "typed-coverage-partial":
				wantRefusal = "coverage=partial"
				mem.typedResults = []string{typedAddendumResult(t, a, func(r *workflowtemplate.ReviewResult) { r.Coverage = "partial" })}
				defer func() { mem.typedResults = nil }()
			case "later-not-clean":
				mem.AddComment(ctx, "/d", "review-task", "Head: "+head+"\nReal unresolved finding\nNOT CLEAN")
			default:
				t.Fatalf("unhandled scenario refusal %q", name)
			}
			before, _ := mem.Comments(ctx, "/d", step)
			if err := checkWorkflowClose(ctx, mem, "/d", "manifest", step); err == nil {
				t.Fatal("invalid addendum passed ordinary close")
			} else if mem.changedBead != "" && !strings.Contains(err.Error(), "attribution changed") {
				t.Fatalf("wrong attribution refusal: %v", err)
			} else if !strings.Contains(err.Error(), wantRefusal) {
				t.Fatalf("refused for the wrong reason, want %q: %v", wantRefusal, err)
			}
			after, _ := mem.Comments(ctx, "/d", step)
			if len(before) != len(after) {
				t.Fatal("refusal wrote workflow-check evidence")
			}
		})
	}
}

func TestWorkflowAddendumArtifactRefusals(t *testing.T) {
	for _, name := range []string{"wrong-head", "wrong-base", "wrong-run", "wrong-manifest", "wrong-binding", "missing-artifact", "altered-prompt", "unanchored-manifest", "missing-mandatory-lens", "actual-source-findings", "failed-validator", "no-peer-receipt", "late-peer-receipt", "linked-transcript"} {
		t.Run(name, func(t *testing.T) {
			a, original := seedAddendumArtifacts(t)
			switch name {
			case "wrong-head":
				a.Head = strings.Repeat("c", 40)
			case "wrong-base":
				a.Base = strings.Repeat("c", 40)
			case "wrong-run":
				a.RunID = "reused-run"
			case "wrong-manifest":
				a.ManifestSHA256 = workflowtemplate.TextDigest("changed")
			case "wrong-binding":
				a.OriginalBindingSHA256 = workflowtemplate.TextDigest("changed")
			case "missing-artifact":
				if e := os.Remove(filepath.Join(filepath.Dir(a.ManifestPath), "testing.events.jsonl")); e != nil {
					t.Fatal(e)
				}
			case "altered-prompt":
				if e := os.WriteFile(filepath.Join(filepath.Dir(a.ManifestPath), "testing.prompt.txt"), []byte("changed"), 0600); e != nil {
					t.Fatal(e)
				}
			case "unanchored-manifest":
				original = "Head: " + a.Head + "\nNOT CLEAN"
			case "missing-mandatory-lens", "actual-source-findings", "failed-validator":
				raw, e := os.ReadFile(a.ManifestPath)
				if e != nil {
					t.Fatal(e)
				}
				var m map[string]any
				json.Unmarshal(raw, &m)
				files := m["files"].(map[string]any)
				if name == "missing-mandatory-lens" {
					delete(files, "testing.events.jsonl")
				} else {
					review := `{"status":"complete","verdict":"Ready to merge","findings":[{"text":"real unresolved finding"}]}`
					if name == "failed-validator" {
						review = `{"status":"complete","verdict":"Ready to merge","findings":[],"testing_gaps":[],"coverage":{"depth":"full","validator":{"outcome":"empty-batch","confirmed":0,"rejected":0,"unresolved":0,"malformed":0,"failed":1,"shortcut_skipped":0}}}`
					}
					os.WriteFile(filepath.Join(filepath.Dir(a.ManifestPath), "review.json"), []byte(review), 0600)
					files["review.json"] = workflowtemplate.TextDigest(review)
				}
				raw, _ = json.Marshal(m)
				os.WriteFile(a.ManifestPath, raw, 0600)
				old := a.ManifestSHA256
				a.ManifestSHA256 = workflowtemplate.TextDigest(string(raw))
				original = strings.ReplaceAll(original, old, a.ManifestSHA256)
			case "no-peer-receipt":
				a.PeerJobID = "missing-job"
			case "late-peer-receipt":
				a.PeerStartEpoch = 300
			case "linked-transcript":
				target := a.TranscriptPath
				a.TranscriptPath = filepath.Join(filepath.Dir(target), "linked")
				if e := os.Symlink(target, a.TranscriptPath); e != nil {
					t.Fatal(e)
				}
			}
			if e := workflowtemplate.VerifyAddendumArtifacts(a, original); e == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
}
