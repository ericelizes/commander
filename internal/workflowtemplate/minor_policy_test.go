package workflowtemplate

import (
	"context"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/reviewpolicy"
	"github.com/example-user/commander/internal/reviewrun"
)

// MinorPolicy: a review whose only findings are minors passes, with those
// minors filed as follow-ups instead of another round.
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

func minor(id string) ReviewFinding {
	return ReviewFinding{ID: id, Severity: "minor", Summary: "synthetic nit " + id}
}

func TestMinorPolicyFilesFollowUpsAndPasses(t *testing.T) {
	review := typedReview("review-1", 7, "operator-first", recheckSHA, strings.Repeat("b", 40), minor("m1"), minor("m2"))
	var filedFor string
	review.FileFollowUps = func(_ context.Context, id, head string, fs []ReviewFinding) ([]string, error) {
		filedFor = id + "@" + head
		ids := make([]string, len(fs))
		for i := range fs {
			ids[i] = "follow-" + fs[i].ID
		}
		return ids, nil
	}
	read := func(context.Context, string) (Review, error) { return review, nil }
	proof := &Proof{ReviewBead: review.ID, Head: recheckSHA, FixCommits: []string{fixSHA}}
	e, err := Verify(context.Background(), loopGH(recheckSHA), read, "review-clean", testURL, "key", "review", proof)
	if err != nil {
		t.Fatalf("review with only minors refused: %v", err)
	}
	if filedFor != "review-1@"+recheckSHA || strings.Join(e.FollowUps, ",") != "follow-m1,follow-m2" || !strings.Contains(e.Finding, MinorPolicy) {
		t.Fatalf("follow-ups not recorded: %q %+v", filedFor, e)
	}
	if got, ok := ParseEvidence(e.Comment()); !ok || len(got.FollowUps) != 2 {
		t.Fatalf("evidence lost the policy record: %+v", got)
	}

	// A filer that fails leaves the step open: no evidence without follow-ups.
	review.FileFollowUps = func(context.Context, string, string, []ReviewFinding) ([]string, error) {
		return nil, context.DeadlineExceeded
	}
	if _, err := Verify(context.Background(), loopGH(recheckSHA), read, "review-clean", testURL, "key", "review", proof); err == nil {
		t.Fatal("passed without filing its follow-ups")
	}
	review.FileFollowUps = nil
	if _, err := Verify(context.Background(), loopGH(recheckSHA), read, "review-clean", testURL, "key", "review", proof); err == nil {
		t.Fatal("passed with no follow-up filer")
	}
}

func TestMinorPolicyNeverPassesBlockersOrHiddenMinors(t *testing.T) {
	filer := func(_ context.Context, _, _ string, fs []ReviewFinding) ([]string, error) {
		return make([]string, len(fs)), nil
	}
	for _, tc := range []struct {
		name   string
		review Review
		proof  Proof
		want   string
	}{
		{"major on the review", typedReview("review-2", 8, "operator-second", recheckSHA, strings.Repeat("b", 40), ReviewFinding{ID: "x", Severity: "major", Summary: "bug"}), Proof{FixCommits: []string{fixSHA}}, "source findings"},
		{"no_findings hiding minors", typedReview("review-2", 8, "operator-second", recheckSHA, strings.Repeat("b", 40), minor("m")), Proof{NoFindings: true}, "contradicts"},
		// A review of only a delta never stands as full coverage.
		{"delta-only review", typedReview("review-2", 8, "operator-second", recheckSHA, priorHead), Proof{NoFindings: true}, "reviewed only the delta"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.review.FileFollowUps = filer
			read := func(context.Context, string) (Review, error) { return tc.review, nil }
			tc.proof.ReviewBead, tc.proof.Head = tc.review.ID, recheckSHA
			_, err := Verify(context.Background(), loopGH(recheckSHA), read, "review-clean", testURL, "key", "review", &tc.proof)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want refusal %q, got %v", tc.want, err)
			}
		})
	}
}

// R1: a review whose base is neither the PR base nor an ancestor of it (a
// pre-force-push head, another branch's commit) covers an unknown range: it
// never stands as a proof. An ancestor of the PR base does, and a compare that
// fails proves nothing.
func TestFullCoverageNeedsThePRBaseOrAnAncestor(t *testing.T) {
	prBase, older, stray := strings.Repeat("b", 40), strings.Repeat("6", 40), strings.Repeat("7", 40)
	gh := func(compare string, compareErr error) GHRunner {
		pr := loopGH(recheckSHA)
		return func(ctx context.Context, args ...string) ([]byte, error) {
			if args[0] == "api" && strings.Contains(args[1], "/compare/") {
				if !strings.HasSuffix(args[1], "..."+prBase) {
					t.Errorf("compare of %s, want <base>...%s", args[1], prBase)
				}
				return []byte(compare), compareErr
			}
			return pr(ctx, args...)
		}
	}
	for _, tc := range []struct {
		name, base, compare string
		compareErr          error
		want                string
	}{
		{"the PR base", prBase, "", nil, ""},
		{"an ancestor of the PR base", older, "ahead", nil, ""},
		{"a commit off the PR's history", stray, "diverged", nil, "neither the PR base"},
		{"a commit after the PR base", stray, "behind", nil, "neither the PR base"},
		{"compare fails", stray, "", context.DeadlineExceeded, "deadline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Plain proof.
			plain := typedReview("review-1", 7, "operator-first", recheckSHA, tc.base)
			plain.FileFollowUps = func(context.Context, string, string, []ReviewFinding) ([]string, error) { return nil, nil }
			read := func(context.Context, string) (Review, error) { return plain, nil }
			_, err := Verify(context.Background(), gh(tc.compare, tc.compareErr), read, "recheck-clean", testURL, "key", "fix-review", &Proof{ReviewBead: plain.ID, Head: recheckSHA, NoFindings: true})
			check(t, "plain proof", err, tc.want)
		})
	}
}

func check(t *testing.T, what string, err error, want string) {
	t.Helper()
	if want == "" && err != nil {
		t.Fatalf("%s refused: %v", what, err)
	}
	if want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
		t.Fatalf("%s: want refusal %q, got %v", what, want, err)
	}
}
