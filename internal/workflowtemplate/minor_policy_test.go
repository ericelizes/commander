package workflowtemplate

import (
	"context"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/reviewpolicy"
	"github.com/example-user/commander/internal/reviewrun"
)

// The bounded review loop (MinorPolicy): a first review finds minors, one fix
// pass, a delta recheck of only the fix finds new minors, and the step passes
// with those minors filed as follow-ups instead of another round.
var (
	priorHead  = strings.Repeat("1", 40)
	recheckSHA = strings.Repeat("2", 40)
	fixSHA     = strings.Repeat("3", 40)
)

func loopGH(head string) GHRunner {
	return func(_ context.Context, args ...string) ([]byte, error) {
		return []byte(`{"headRefOid":"` + head + `","baseRefOid":"` + strings.Repeat("b", 40) + `","state":"OPEN","commits":[{"oid":"` + priorHead + `"},{"oid":"` + fixSHA + `"},{"oid":"` + head + `"}]}`), nil
	}
}

// typedReview is a closed review bead with one valid typed result.
func typedReview(id string, run int64, actor, head, base string, findings ...ReviewFinding) Review {
	r := ReviewResult{Task: id, RunID: run, Actor: actor, Head: head, Base: base, ScopeHash: strings.Repeat("c", 64), ManifestHash: strings.Repeat(string(rune('d'+run%2)), 64), Depth: "focused", Execution: "complete", Source: "clear", Coverage: "complete", Findings: []ReviewFinding{}}
	if len(findings) > 0 {
		r.Source, r.Findings = "findings", findings
	}
	rec := map[string]reviewrun.Recorded{r.ManifestHash: {Manifest: reviewrun.Manifest{Version: "review-run@v1", Task: id, RunID: run, Actor: actor, Policy: reviewpolicy.Result{Base: base, Head: head, ScopeHash: r.ScopeHash, Depth: "focused"}}, Terminal: reviewrun.Result{Execution: "complete"}}}
	return Review{ID: id, Status: "closed", Owner: actor, Runs: map[int64]string{run: actor}, Recorded: rec, Comments: []string{"Head: " + head + "\nCLEAN", r.Comment()}}
}

// resolving makes r's typed receipt list the prior findings it verified fixed.
func resolving(r Review, ids ...string) Review {
	last := len(r.Comments) - 1
	res, _, _ := ParseReviewResult(r.Comments[last])
	res.Resolved = ids
	r.Comments = append(append([]string{}, r.Comments[:last]...), res.Comment())
	return r
}

func minor(id string) ReviewFinding {
	return ReviewFinding{ID: id, Severity: "minor", Summary: "synthetic nit " + id}
}

func TestMinorPolicyDeltaRecheckFilesFollowUpsAndPasses(t *testing.T) {
	first := typedReview("review-1", 7, "operator-first", priorHead, strings.Repeat("b", 40), minor("m1"), minor("m2"))
	recheck := resolving(typedReview("review-2", 8, "operator-second", recheckSHA, priorHead, minor("m3")), "m1")
	var filedFor string
	recheck.FileFollowUps = func(_ context.Context, review, head string, fs []ReviewFinding) ([]string, error) {
		filedFor = review + "@" + head
		ids := make([]string, len(fs))
		for i := range fs {
			ids[i] = "follow-" + fs[i].ID
		}
		return ids, nil
	}
	read := func(_ context.Context, id string) (Review, error) {
		if id == first.ID {
			return first, nil
		}
		return recheck, nil
	}
	proof := &Proof{ReviewBead: recheck.ID, Head: recheckSHA, FixCommits: []string{fixSHA}, PriorReview: first.ID, PriorHead: priorHead}
	e, err := Verify(context.Background(), loopGH(recheckSHA), read, "recheck-clean", testURL, "key", "fix-review", proof)
	if err != nil {
		t.Fatalf("delta recheck with only minors after one fix pass refused: %v", err)
	}
	if filedFor != "review-2@"+recheckSHA || strings.Join(e.FollowUps, ",") != "follow-m3" || !strings.Contains(e.Finding, MinorPolicy) {
		t.Fatalf("follow-ups not recorded: %q %+v", filedFor, e)
	}
	if got, ok := ParseEvidence(e.Comment()); !ok || len(got.FollowUps) != 1 || got.Proof.PriorHead != priorHead {
		t.Fatalf("evidence lost the policy record: %+v", got)
	}

	// A filer that fails leaves the step open: no evidence without follow-ups.
	recheck.FileFollowUps = func(context.Context, string, string, []ReviewFinding) ([]string, error) {
		return nil, context.DeadlineExceeded
	}
	if _, err := Verify(context.Background(), loopGH(recheckSHA), read, "recheck-clean", testURL, "key", "fix-review", proof); err == nil {
		t.Fatal("passed without filing its follow-ups")
	}
	recheck.FileFollowUps = nil
	if _, err := Verify(context.Background(), loopGH(recheckSHA), read, "recheck-clean", testURL, "key", "fix-review", proof); err == nil {
		t.Fatal("passed with no follow-up filer")
	}
}

func TestMinorPolicyNeverPassesBlockersOrDishonestDeltas(t *testing.T) {
	first := typedReview("review-1", 7, "operator-first", priorHead, strings.Repeat("b", 40), ReviewFinding{ID: "b1", Severity: "blocker", Summary: "data loss"})
	filer := func(_ context.Context, _, _ string, fs []ReviewFinding) ([]string, error) {
		return make([]string, len(fs)), nil
	}
	for _, tc := range []struct {
		name   string
		review Review
		proof  Proof
		want   string
	}{
		{"major on the recheck", typedReview("review-2", 8, "operator-second", recheckSHA, priorHead, ReviewFinding{ID: "x", Severity: "major", Summary: "bug"}), Proof{FixCommits: []string{fixSHA}, PriorReview: "review-1", PriorHead: priorHead}, "source findings"},
		{"recheck of the whole PR claimed as a delta", typedReview("review-2", 8, "operator-second", recheckSHA, strings.Repeat("b", 40)), Proof{FixCommits: []string{fixSHA}, PriorReview: "review-1", PriorHead: priorHead}, "fix delta"},
		{"no fix pass", typedReview("review-2", 8, "operator-second", recheckSHA, priorHead), Proof{NoFindings: true, PriorReview: "review-1", PriorHead: priorHead}, "fix_commits"},
		{"prior head is the current head", typedReview("review-2", 8, "operator-second", recheckSHA, priorHead), Proof{FixCommits: []string{fixSHA}, PriorReview: "review-1", PriorHead: recheckSHA}, "prior_head"},
		{"prior review is the recheck", typedReview("review-2", 8, "operator-second", recheckSHA, priorHead), Proof{FixCommits: []string{fixSHA}, PriorReview: "review-2", PriorHead: priorHead}, "not review"},
		{"prior head with no retained coverage", typedReview("review-2", 8, "operator-second", recheckSHA, strings.Repeat("4", 40)), Proof{FixCommits: []string{fixSHA}, PriorReview: "review-1", PriorHead: strings.Repeat("4", 40)}, "no complete typed review"},
		{"no_findings hiding minors", typedReview("review-2", 8, "operator-second", recheckSHA, priorHead, minor("m")), Proof{NoFindings: true}, "contradicts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.review.FileFollowUps = filer
			read := func(_ context.Context, id string) (Review, error) {
				if id == first.ID {
					return first, nil
				}
				return tc.review, nil
			}
			tc.proof.ReviewBead, tc.proof.Head = tc.review.ID, recheckSHA
			_, err := Verify(context.Background(), loopGH(recheckSHA), read, "recheck-clean", testURL, "key", "fix-review", &tc.proof)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want refusal %q, got %v", tc.want, err)
			}
		})
	}
	// A clear delta recheck that never verified the prior blocker fixed (a fix
	// commit elsewhere, the blocker's code unchanged and so not re-reviewed)
	// passes nothing; one that names it in resolved does.
	proof := &Proof{ReviewBead: "review-2", Head: recheckSHA, FixCommits: []string{fixSHA}, PriorReview: first.ID, PriorHead: priorHead}
	for _, tc := range []struct {
		name  string
		clear Review
		want  string
	}{
		{"blocker not verified fixed", typedReview("review-2", 8, "operator-second", recheckSHA, priorHead), "b1"},
		{"resolves a finding the prior never had", resolving(typedReview("review-2", 8, "operator-second", recheckSHA, priorHead), "b1", "zz"), "no finding of prior review"},
		{"blocker verified fixed", resolving(typedReview("review-2", 8, "operator-second", recheckSHA, priorHead), "b1"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := func(_ context.Context, id string) (Review, error) {
				if id == first.ID {
					return first, nil
				}
				return tc.clear, nil
			}
			_, err := Verify(context.Background(), loopGH(recheckSHA), read, "recheck-clean", testURL, "key", "fix-review", proof)
			if tc.want == "" && err != nil {
				t.Fatalf("delta recheck that verified the blocker fixed refused: %v", err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("want refusal %q, got %v", tc.want, err)
			}
		})
	}
}

// A delta-only review never stands as full coverage: not as a plain proof (M1),
// not as the retained prior of a recheck (M2, no chains), not as a renewal.
func TestMinorPolicyDeltaReviewsNeverStandAlone(t *testing.T) {
	full := typedReview("review-1", 7, "operator-first", priorHead, strings.Repeat("b", 40))
	delta := typedReview("review-2", 8, "operator-second", recheckSHA, priorHead)
	read := func(_ context.Context, id string) (Review, error) {
		if id == full.ID {
			return full, nil
		}
		return delta, nil
	}
	// M1: the delta recheck's receipt offered as a plain proof.
	_, err := Verify(context.Background(), loopGH(recheckSHA), read, "recheck-clean", testURL, "key", "fix-review", &Proof{ReviewBead: delta.ID, Head: recheckSHA, NoFindings: true})
	if err == nil || !strings.Contains(err.Error(), "reviewed only the delta") {
		t.Fatalf("a delta-only review passed as a plain proof: %v", err)
	}
	// M2: a second recheck retaining the first recheck (itself a delta).
	third := strings.Repeat("5", 40)
	gh := func(_ context.Context, args ...string) ([]byte, error) {
		return []byte(`{"headRefOid":"` + third + `","baseRefOid":"` + strings.Repeat("b", 40) + `","state":"OPEN","commits":[{"oid":"` + priorHead + `"},{"oid":"` + fixSHA + `"},{"oid":"` + recheckSHA + `"},{"oid":"` + third + `"}]}`), nil
	}
	chained := typedReview("review-3", 9, "operator-third", third, recheckSHA)
	readChain := func(_ context.Context, id string) (Review, error) {
		switch id {
		case full.ID:
			return full, nil
		case delta.ID:
			return delta, nil
		}
		return chained, nil
	}
	_, err = Verify(context.Background(), gh, readChain, "recheck-clean", testURL, "key", "fix-review", &Proof{ReviewBead: chained.ID, Head: third, FixCommits: []string{recheckSHA}, PriorReview: delta.ID, PriorHead: recheckSHA})
	if err == nil || !strings.Contains(err.Error(), "never another delta") {
		t.Fatalf("a recheck chained onto another recheck passed: %v", err)
	}
	// The way out: the later recheck reviews everything since the full review.
	since := typedReview("review-3", 9, "operator-third", third, priorHead)
	readSince := func(_ context.Context, id string) (Review, error) {
		if id == full.ID {
			return full, nil
		}
		return since, nil
	}
	if _, err = Verify(context.Background(), gh, readSince, "recheck-clean", testURL, "key", "fix-review", &Proof{ReviewBead: since.ID, Head: third, FixCommits: []string{fixSHA, recheckSHA}, PriorReview: full.ID, PriorHead: priorHead}); err != nil {
		t.Fatalf("a recheck of all fixes since the full review refused: %v", err)
	}
	// A renewal carries a full review only.
	docs := `{"status":"ahead","files":[{"filename":"docs/runbook.md","status":"modified","additions":1,"deletions":1,"patch":"@@ -1 +1 @@\n-Run the job nightly.\n+Run the job every night."}]}`
	renewGH := func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "api" && strings.Contains(args[1], "/compare/") {
			return []byte(docs), nil
		}
		return gh(ctx, args...)
	}
	_, err = Verify(context.Background(), renewGH, readChain, "recheck-clean", testURL, "key", "fix-review", &Proof{ReviewBead: delta.ID, Head: third, FixCommits: []string{third}, PriorHead: recheckSHA, DeltaRenewal: true})
	if err == nil || !strings.Contains(err.Error(), "reviewed only the delta") {
		t.Fatalf("a renewal carried a delta-only review: %v", err)
	}
	// Naming the full review it retained renews the recheck.
	if _, err = Verify(context.Background(), renewGH, readChain, "recheck-clean", testURL, "key", "fix-review", &Proof{ReviewBead: delta.ID, Head: third, FixCommits: []string{third}, PriorHead: recheckSHA, PriorReview: full.ID, DeltaRenewal: true}); err != nil {
		t.Fatalf("a renewal of a recheck naming its full review refused: %v", err)
	}
}

// renewalGH answers the PR read like loopGH and the compare of the fix delta
// with the given JSON (already shaped like the --jq output).
func renewalGH(head, compare string, compareErr error) GHRunner {
	pr := loopGH(head)
	return func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "api" && strings.Contains(args[1], "/compare/") {
			return []byte(compare), compareErr
		}
		return pr(ctx, args...)
	}
}

func TestDeltaRenewalCarriesACleanReviewOverATinyDocsFix(t *testing.T) {
	clean := typedReview("review-1", 7, "operator-first", priorHead, strings.Repeat("b", 40))
	read := func(context.Context, string) (Review, error) { return clean, nil }
	proof := func() *Proof {
		return &Proof{ReviewBead: clean.ID, Head: recheckSHA, FixCommits: []string{fixSHA}, PriorHead: priorHead, DeltaRenewal: true}
	}
	docs := `{"status":"ahead","files":[{"filename":"docs/runbook.md","status":"modified","additions":1,"deletions":1,"patch":"@@ -1 +1 @@\n-Run the job nightly.\n+Run the job every night."}]}`
	e, err := Verify(context.Background(), renewalGH(recheckSHA, docs, nil), read, "recheck-clean", testURL, "key", "fix-review", proof())
	if err != nil {
		t.Fatalf("a one-line runbook fix after a clean review needed a new review: %v", err)
	}
	if e.Head != recheckSHA || !strings.Contains(e.Finding, "delta renewal of "+priorHead) || !strings.Contains(e.Finding, "docs/runbook.md") {
		t.Fatalf("renewal not recorded honestly: %+v", e)
	}
	for _, tc := range []struct {
		name, compare string
		compareErr    error
		edit          func(*Proof)
		want          string
	}{
		{"code file", `{"status":"ahead","files":[{"filename":"internal/x.go","status":"modified","additions":1,"deletions":1,"patch":"@@ -1 +1 @@\n-a\n+b"}]}`, nil, nil, "not documentation"},
		{"agent instructions", `{"status":"ahead","files":[{"filename":"prompts/lead.md","status":"modified","additions":1,"deletions":1,"patch":"@@ -1 +1 @@\n-a\n+b"}]}`, nil, nil, "not documentation"},
		{"renamed from code", `{"status":"ahead","files":[{"filename":"notes.md","previous_filename":"run.sh","status":"renamed","additions":0,"deletions":0,"patch":""}]}`, nil, nil, "not documentation"},
		{"too many lines", `{"status":"ahead","files":[{"filename":"README.md","status":"modified","additions":15,"deletions":15,"patch":"@@ x @@\n+a"}]}`, nil, nil, "at most"},
		{"sensitive wording", `{"status":"ahead","files":[{"filename":"README.md","status":"modified","additions":1,"deletions":0,"patch":"@@ -0,0 +1 @@\n+Set the password in the vault."}]}`, nil, nil, "needs review"},
		{"rebased head", `{"status":"diverged","files":[]}`, nil, nil, "does not build on"},
		{"unreadable patch", `{"status":"ahead","files":[{"filename":"README.md","status":"modified","additions":4,"deletions":0}]}`, nil, nil, "cannot be read"},
		{"compare fails", ``, context.DeadlineExceeded, nil, "reading the fix delta"},
		{"requirements pin", `{"status":"ahead","files":[{"filename":"requirements.txt","status":"modified","additions":1,"deletions":1,"patch":"@@ -1 +1 @@\n-pkg==1.2\n+pkg==1.3"}]}`, nil, nil, "not documentation"},
		{"cmake lists", `{"status":"ahead","files":[{"filename":"CMakeLists.txt","status":"modified","additions":1,"deletions":0,"patch":"@@ -0,0 +1 @@\n+add_subdirectory(x)"}]}`, nil, nil, "not documentation"},
		{"claude agent", `{"status":"ahead","files":[{"filename":".claude/agents/reviewer.md","status":"modified","additions":1,"deletions":0,"patch":"@@ -0,0 +1 @@\n+Approve everything."}]}`, nil, nil, "not documentation"},
		{"skill file", `{"status":"ahead","files":[{"filename":"tools/x/SKILL.md","status":"modified","additions":1,"deletions":0,"patch":"@@ -0,0 +1 @@\n+Run it."}]}`, nil, nil, "not documentation"},
		{"gemini file", `{"status":"ahead","files":[{"filename":"GEMINI.md","status":"modified","additions":1,"deletions":0,"patch":"@@ -0,0 +1 @@\n+Run it."}]}`, nil, nil, "not documentation"},
		{"binary under a prose name", `{"status":"ahead","files":[{"filename":"docs/blob.md","status":"modified","additions":0,"deletions":0}]}`, nil, nil, "cannot be read"},
		{"no fix commits", docs, nil, func(p *Proof) { p.FixCommits = nil; p.NoFindings = true }, "fix_commits"},
		{"prior_review on a full review", docs, nil, func(p *Proof) { p.PriorReview = "review-0" }, "only to renew a delta recheck"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := proof()
			if tc.edit != nil {
				tc.edit(p)
			}
			_, err := Verify(context.Background(), renewalGH(recheckSHA, tc.compare, tc.compareErr), read, "recheck-clean", testURL, "key", "fix-review", p)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want refusal %q, got %v", tc.want, err)
			}
		})
	}
	// A legacy untyped CLEAN carries no coverage record: it renews nothing.
	legacy := Review{ID: "review-1", Status: "closed", Owner: "operator-first", Comments: []string{"Head: " + priorHead + "\nCLEAN"}}
	readLegacy := func(context.Context, string) (Review, error) { return legacy, nil }
	if _, err := Verify(context.Background(), renewalGH(recheckSHA, docs, nil), readLegacy, "recheck-clean", testURL, "key", "fix-review", proof()); err == nil || !strings.Contains(err.Error(), "typed review") {
		t.Fatalf("renewed a legacy untyped review: %v", err)
	}
	// A review that was not clean on the prior head renews nothing.
	bad := typedReview("review-1", 7, "operator-first", priorHead, strings.Repeat("b", 40), ReviewFinding{ID: "b", Severity: "blocker", Summary: "bug"})
	readBad := func(context.Context, string) (Review, error) { return bad, nil }
	if _, err := Verify(context.Background(), renewalGH(recheckSHA, docs, nil), readBad, "recheck-clean", testURL, "key", "fix-review", proof()); err == nil {
		t.Fatal("renewed a review with a blocker")
	}
}

// proseFile is an allow-list: docs and plain markup only, never build, config
// or agent files (M5 of the bounded loop's review).
func TestDeltaRenewalProseAllowList(t *testing.T) {
	for p, want := range map[string]bool{
		"README.md": true, "docs/runbook.md": true, "docs/notes.txt": true, "guide/setup.rst": true, "CHANGELOG.markdown": true,
		"requirements.txt": false, "CMakeLists.txt": false, "notes.txt": false, "src/LICENSE.txt": false,
		".claude/agents/reviewer.md": false, ".github/copilot-instructions.md": false, ".cursor/rules/x.md": false,
		"prompts/lead.md": false, "app/prompts/x.md": false, "agents/review.md": false, "skills/x/README.md": false,
		"CLAUDE.md": false, "docs/AGENTS.md": false, "GEMINI.md": false, "tools/SKILL.md": false, ".cursorrules": false,
		"internal/x.go": false, "Makefile": false, "docs/../x.md": false,
	} {
		if got := proseFile(p); got != want {
			t.Errorf("proseFile(%q) = %v, want %v", p, got, want)
		}
	}
}
