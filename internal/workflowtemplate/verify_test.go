package workflowtemplate

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testURL = "https://github.com/o/r/pull/7"

func fakeGH(head string, unresolved bool) GHRunner {
	return func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "pr" {
			return []byte(`{"headRefOid":"` + head + `","baseRefOid":"` + strings.Repeat("b", 40) + `","state":"MERGED","author":{"login":"writer"},"commits":[{"oid":"fix"}],"statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"}]}`), nil
		}
		if args[0] == "run" {
			return []byte(`[{"headSha":"` + head + `","status":"completed","conclusion":"success"}]`), nil
		}
		resolved := "true"
		if unresolved {
			resolved = "false"
		}
		return []byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"isResolved":` + resolved + `,"comments":{"totalCount":2,"nodes":[{"author":{"login":"devin-ai"}}]}}],"pageInfo":{"hasNextPage":false}}}}}}`), nil
	}
}
func TestVerifierChecks(t *testing.T) {
	read := func(context.Context, string) (Review, error) {
		return Review{ID: "review-bead", Status: "closed", Owner: "operator-reviewer", Comments: []string{"Head: abc\nno findings\nCLEAN"}}, nil
	}
	proof := &Proof{ReviewBead: "review-bead", Head: "abc", FixCommits: []string{"fix"}}
	for _, id := range Verifiers {
		t.Run(id, func(t *testing.T) {
			e, err := Verify(context.Background(), fakeGH("abc", false), read, id, testURL, "key", "step", proof)
			if err != nil {
				t.Fatal(err)
			}
			if e.Head != "abc" || e.Finding == "" {
				t.Fatal(e)
			}
			if _, ok := ParseEvidence(e.Comment()); !ok {
				t.Fatal("bad evidence")
			}
			_, err = Verify(context.Background(), func(context.Context, ...string) ([]byte, error) { return nil, errors.New("offline") }, read, id, testURL, "key", "step", proof)
			if err == nil {
				t.Fatal("offline accepted")
			}
		})
	}
}
func TestVerifierRefusals(t *testing.T) {
	cases := []struct {
		name, id string
		run      GHRunner
		proof    *Proof
		read     ReviewReader
	}{
		{name: "unresolved", id: "threads-resolved", run: fakeGH("abc", true)},
		{name: "no proof", id: "review-proof", run: fakeGH("abc", false)},
		{name: "old proof", id: "review-proof", run: fakeGH("new", false), proof: &Proof{ReviewBead: "r", Head: "old", NoFindings: true}},
		{name: "no CI", id: "ci-green", run: func(context.Context, ...string) ([]byte, error) {
			return []byte(`{"headRefOid":"abc","state":"OPEN"}`), nil
		}},
		{name: "pending CI", id: "ci-green", run: func(context.Context, ...string) ([]byte, error) {
			return []byte(`{"headRefOid":"abc","state":"OPEN","statusCheckRollup":[{"status":"IN_PROGRESS"}]}`), nil
		}},
		{name: "not merged", id: "merged", run: func(context.Context, ...string) ([]byte, error) {
			return []byte(`{"headRefOid":"abc","state":"OPEN"}`), nil
		}},
		{name: "deploy absent", id: "deploy-ok", run: func(ctx context.Context, a ...string) ([]byte, error) {
			if a[0] == "run" {
				return []byte(`[]`), nil
			}
			return fakeGH("abc", false)(ctx, a...)
		}},
		{name: "malformed threads", id: "threads-resolved", run: func(ctx context.Context, a ...string) ([]byte, error) {
			if a[0] == "api" {
				return []byte(`{}`), nil
			}
			return fakeGH("abc", false)(ctx, a...)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Verify(context.Background(), c.run, c.read, c.id, testURL, "k", "s", c.proof)
			if err == nil {
				t.Fatal("accepted")
			}
		})
	}
	calls := 0
	run := func(ctx context.Context, a ...string) ([]byte, error) {
		head := "abc"
		if a[0] == "pr" {
			calls++
			if calls > 1 {
				head = "new"
			}
		}
		return fakeGH(head, false)(ctx, a...)
	}
	if _, err := Verify(context.Background(), run, nil, "head-current", testURL, "k", "s", nil); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatal(err)
	}
}
func TestThreadPagination(t *testing.T) {
	calls := 0
	run := func(ctx context.Context, a ...string) ([]byte, error) {
		if a[0] != "api" {
			return fakeGH("abc", false)(ctx, a...)
		}
		calls++
		if calls == 1 {
			return []byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"next"}}}}}}`), nil
		}
		if !strings.Contains(strings.Join(a, " "), "cursor=next") {
			t.Fatal(a)
		}
		return fakeGH("abc", true)(ctx, a...)
	}
	if _, err := Verify(context.Background(), run, nil, "threads-resolved", testURL, "k", "s", nil); err == nil || calls != 2 {
		t.Fatal(err, calls)
	}
}
func TestReviewProofMustBeCleanAndFixesOnPR(t *testing.T) {
	p := &Proof{ReviewBead: "r", Head: "abc", FixCommits: []string{"missing"}}
	read := func(context.Context, string) (Review, error) {
		return Review{ID: "r", Status: "closed", Owner: "operator-reviewer", Comments: []string{"Head: abc\nCLEAN"}}, nil
	}
	if _, err := Verify(context.Background(), fakeGH("abc", false), read, "review-proof", testURL, "k", "s", p); err == nil {
		t.Fatal("unknown fix accepted")
	}
	p.FixCommits = []string{"fix"}
	read = func(context.Context, string) (Review, error) {
		return Review{ID: "r", Status: "closed", Owner: "operator-reviewer", Comments: []string{"NOT CLEAN"}}, nil
	}
	if _, err := Verify(context.Background(), fakeGH("abc", false), read, "review-proof", testURL, "k", "s", p); err == nil {
		t.Fatal("unclean accepted")
	}
}

// F8, F3: the report is the latest comment with a verdict line.
func TestReviewVerdict(t *testing.T) {
	const head = "abc"
	for _, c := range []struct {
		name     string
		comments []string
		want     string // refusal text, "" = accepted
	}{
		{"report", []string{"Head: abc\nCLEAN"}, ""},
		{"later notes do not unmake it", []string{"Head: abc\nCLEAN", "note: mailed the Manager"}, ""},
		{"skill output before the report", []string{"raw skill output", "Head: abc\nCLEAN"}, ""},
		{"verdict is not the last line", []string{"Head: abc\nCLEAN\nRun dir: /tmp/x"}, ""},
		{"earlier head was not clean", []string{"Head: old\nNOT CLEAN", "Head: abc\nCLEAN"}, ""},
		{"no verdict", []string{"Head: abc, looks fine"}, "no CLEAN or NOT CLEAN"},
		{"a word in prose is no verdict", []string{"Head: abc: not CLEAN yet, CLEAN soon"}, "no CLEAN or NOT CLEAN"},
		{"markdown head line", []string{"- **Head:** `abc`\nCLEAN"}, ""},
		{"short or trailing head is no head", []string{"Head: ab\nHead: abc (tip)\nCLEAN"}, "does not identify"},
		{"no head", []string{"CLEAN"}, "Head: <full-sha>"},
		{"head only in another comment", []string{"Head: abc", "CLEAN"}, "does not identify"},
		{"sha only in prose", []string{"Reviewed abc; looks fine\nCLEAN"}, "does not identify"},
		{"not clean", []string{"Head: abc\nNOT CLEAN"}, "did not end CLEAN"},
		{"clean appended in the report", []string{"Head: abc\nNOT CLEAN\nCLEAN"}, "did not end CLEAN"},
		{"not clean after clean", []string{"Head: abc\nCLEAN", "Head: abc\nNOT CLEAN"}, "did not end CLEAN"},
		{"clean appended after not clean", []string{"Head: abc\nNOT CLEAN", "Head: abc\nCLEAN"}, "reported NOT CLEAN"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := reviewVerdict(Review{ID: "r", Comments: c.comments}, head)
			if c.want == "" && err != nil {
				t.Fatal(err)
			}
			if c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Fatalf("want %q, got %v", c.want, err)
			}
		})
	}
}

func TestCanonicalPR(t *testing.T) {
	pr, err := CanonicalPR("https://GITHUB.COM/Acme/Repo/pull/7/?x=y#z")
	if err != nil || pr != "https://github.com/acme/repo/pull/7" {
		t.Fatal(pr, err)
	}
	for _, s := range []string{"http://github.com/o/r/pull/7", "https://other.test/o/r/pull/7", "https://github.com/o/r/pull/0"} {
		if _, err := CanonicalPR(s); err == nil {
			t.Fatal(s)
		}
	}
}
func TestEvidenceHeadBinding(t *testing.T) {
	e := Evidence{Instance: "i", Step: "final-check", Verifier: "head-current", PR: testURL, Head: "old", Finding: "passed"}
	raw, _ := json.Marshal(e)
	if !strings.HasPrefix(e.Comment(), EvidencePrefix) || !strings.Contains(e.Comment(), string(raw)) {
		t.Fatal(e.Comment())
	}
}

func TestCIGreen(t *testing.T) {
	type c = struct{ status, conclusion, state string }
	ok := c{"COMPLETED", "SUCCESS", ""}
	tests := []struct {
		name   string
		checks []c
		green  bool
	}{
		{"none", nil, false},
		{"success", []c{ok}, true},
		{"status context success", []c{{state: "SUCCESS"}}, true},
		{"skipped beside a success", []c{ok, {"COMPLETED", "SKIPPED", ""}}, true},
		{"neutral beside a success", []c{ok, {"COMPLETED", "NEUTRAL", ""}}, true},
		{"skipped beside a status context success", []c{{state: "SUCCESS"}, {"COMPLETED", "SKIPPED", ""}}, true},
		{"all skipped", []c{{"COMPLETED", "SKIPPED", ""}, {"COMPLETED", "SKIPPED", ""}}, false},
		{"all neutral", []c{{"COMPLETED", "NEUTRAL", ""}}, false},
		{"skipped and neutral", []c{{"COMPLETED", "SKIPPED", ""}, {"COMPLETED", "NEUTRAL", ""}}, false},
		{"failure", []c{ok, {"COMPLETED", "FAILURE", ""}}, false},
		{"cancelled", []c{ok, {"COMPLETED", "CANCELLED", ""}}, false},
		{"timed out", []c{ok, {"COMPLETED", "TIMED_OUT", ""}}, false},
		{"action required", []c{ok, {"COMPLETED", "ACTION_REQUIRED", ""}}, false},
		{"completed with no conclusion", []c{ok, {"COMPLETED", "", ""}}, false},
		{"in progress", []c{ok, {"IN_PROGRESS", "", ""}}, false},
		{"queued", []c{ok, {"QUEUED", "", ""}}, false},
		{"skipped not completed", []c{ok, {"IN_PROGRESS", "SKIPPED", ""}}, false},
		{"status context pending", []c{ok, {state: "PENDING"}}, false},
		{"status context failure", []c{ok, {state: "FAILURE"}}, false},
		{"status context error", []c{ok, {state: "ERROR"}}, false},
		{"status context skipped is no state", []c{ok, {state: "SKIPPED"}}, false},
		{"failing state with a skipped conclusion", []c{ok, {"COMPLETED", "SKIPPED", "FAILURE"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h Head
			for _, x := range tt.checks {
				h.Checks = append(h.Checks, Check{Status: x.status, Conclusion: x.conclusion, State: x.state})
			}
			if err := ciGreen(h); (err == nil) != tt.green {
				t.Fatalf("green=%v, err=%v", tt.green, err)
			}
		})
	}
}

// A PR whose checks pass with some skipped, through the real gh JSON shape, and
// deploy-ok stays strict about its one run.
func TestVerifyAcceptsSkippedCIAndDeployStaysStrict(t *testing.T) {
	skipped := func(ctx context.Context, a ...string) ([]byte, error) {
		switch a[0] {
		case "pr":
			return []byte(`{"headRefOid":"abc","state":"OPEN","statusCheckRollup":[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"},{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SKIPPED"}]}`), nil
		case "run":
			return []byte(`[{"headSha":"abc","status":"completed","conclusion":"skipped"}]`), nil
		}
		return fakeGH("abc", false)(ctx, a...)
	}
	for _, id := range []string{"ci-green", "head-current"} {
		if _, err := Verify(context.Background(), skipped, nil, id, testURL, "key", "step", nil); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	if _, err := Verify(context.Background(), skipped, nil, "deploy-ok", testURL, "key", "step", nil); err == nil {
		t.Fatal("a skipped deploy passed deploy-ok")
	}
}

// longPR fakes a PR of 300 commits (p1..p300 on top of base b0; main went on
// to b1), as GitHub answers it: `pr view` lists only the oldest 100 commits and
// the compare API reports ancestry. failCompare makes every compare fail.
func longPR(failCompare bool) GHRunner {
	parent := map[string]string{"b0": "a0", "b1": "b0", "p1": "b0"}
	var commits []string
	for i := 1; i <= 300; i++ {
		sha := "p" + strconv.Itoa(i)
		if i > 1 {
			parent[sha] = "p" + strconv.Itoa(i-1)
		}
		if i <= 100 {
			commits = append(commits, `{"oid":"`+sha+`"}`)
		}
	}
	known := func(s string) bool { _, ok := parent[s]; return ok || s == "a0" }
	ancestor := func(a, b string) bool {
		for c := b; c != ""; c = parent[c] {
			if c == a {
				return true
			}
		}
		return false
	}
	return func(_ context.Context, a ...string) ([]byte, error) {
		switch {
		case a[0] == "pr":
			return []byte(`{"headRefOid":"p300","baseRefOid":"b1","state":"OPEN","commits":[` + strings.Join(commits, ",") + `]}`), nil
		case a[0] == "api" && strings.HasPrefix(a[1], "repos/o/r/compare/"):
			if failCompare {
				return nil, errors.New("gh: HTTP 502")
			}
			from, to, _ := strings.Cut(strings.TrimPrefix(a[1], "repos/o/r/compare/"), "...")
			if !known(from) || !known(to) {
				return nil, errors.New("gh: Not Found (HTTP 404)")
			}
			switch {
			case from == to:
				return []byte("identical\n"), nil
			case ancestor(from, to):
				return []byte("ahead\n"), nil
			case ancestor(to, from):
				return []byte("behind\n"), nil
			}
			return []byte("diverged\n"), nil
		}
		return nil, errors.New("unexpected gh call " + strings.Join(a, " "))
	}
}

func TestFixCommitOnALongPR(t *testing.T) {
	read := func(context.Context, string) (Review, error) {
		return Review{ID: "r", Status: "closed", Owner: "operator-reviewer", Comments: []string{"Head: p300\nCLEAN"}}, nil
	}
	check := func(run GHRunner, fix string) error {
		_, err := Verify(context.Background(), run, read, "review-proof", testURL, "k", "s", &Proof{ReviewBead: "r", Head: "p300", FixCommits: []string{fix}})
		return err
	}
	for _, fix := range []string{"p300", "p250", "p101", "p40"} {
		if err := check(longPR(false), fix); err != nil {
			t.Errorf("fix %s on the PR refused: %v", fix, err)
		}
	}
	for _, fix := range []string{"b0", "b1", "a0", "elsewhere"} {
		if err := check(longPR(false), fix); err == nil {
			t.Errorf("fix %s off the PR accepted", fix)
		}
	}
	if err := check(longPR(true), "p250"); err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("failed read accepted: %v", err)
	}
	if err := check(longPR(true), "p40"); err != nil {
		t.Errorf("a commit in the listed 100 needs no compare: %v", err)
	}
}

// Only the latest run of each check counts, as gh pr checks shows them.
func TestCIGreenJudgesTheLatestRunPerCheck(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	run := func(name, status, conclusion string, started int, id string) Check {
		c := Check{Kind: "CheckRun", Workflow: "ci", Name: name, Status: status, Conclusion: conclusion,
			StartedAt: t0.Add(time.Duration(started) * time.Minute), DetailsURL: "https://github.com/acme/widgets/actions/runs/7/job/" + id}
		if status == "COMPLETED" {
			c.CompletedAt = c.StartedAt.Add(time.Minute)
		}
		return c
	}
	ctx := func(name, state string, started int) Check {
		return Check{Kind: "StatusContext", Context: name, State: state, StartedAt: t0.Add(time.Duration(started) * time.Minute)}
	}
	tests := []struct {
		name   string
		checks []Check
		green  bool
	}{
		{"superseded failure then success", []Check{run("test", "COMPLETED", "FAILURE", 0, "11"), run("test", "COMPLETED", "SUCCESS", 5, "12")}, true},
		{"success listed before its older failure", []Check{run("test", "COMPLETED", "SUCCESS", 5, "12"), run("test", "COMPLETED", "FAILURE", 0, "11")}, true},
		{"latest failure", []Check{run("test", "COMPLETED", "SUCCESS", 0, "11"), run("test", "COMPLETED", "FAILURE", 5, "12")}, false},
		{"latest in progress", []Check{run("test", "COMPLETED", "SUCCESS", 0, "11"), run("test", "IN_PROGRESS", "", 5, "12")}, false},
		{"two checks, one fails", []Check{run("test", "COMPLETED", "SUCCESS", 0, "11"), run("lint", "COMPLETED", "FAILURE", 0, "13")}, false},
		{"same job name in another workflow fails", []Check{run("test", "COMPLETED", "SUCCESS", 0, "11"), func() Check {
			c := run("test", "COMPLETED", "FAILURE", 5, "14")
			c.Workflow = "nightly"
			return c
		}()}, false},
		{"equal times, higher run id wins", []Check{run("test", "COMPLETED", "SUCCESS", 0, "12"), run("test", "COMPLETED", "FAILURE", 0, "11")}, true},
		{"status context re-run green", []Check{ctx("ci/build", "FAILURE", 0), ctx("ci/build", "SUCCESS", 5)}, true},
		{"status context latest pending", []Check{ctx("ci/build", "SUCCESS", 0), ctx("ci/build", "PENDING", 5)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ciGreen(Head{SHA: "abc", Checks: tt.checks}); (err == nil) != tt.green {
				t.Fatalf("green=%v, err=%v", tt.green, err)
			}
		})
	}
}

// The same through the gh JSON shape and Verify: a failed run re-run green passes
// ci-green and head-current; a re-run still going refuses both.
func TestVerifyCIReadsTheLatestRun(t *testing.T) {
	rollup := func(second string) GHRunner {
		return func(ctx context.Context, a ...string) ([]byte, error) {
			if a[0] == "pr" && a[1] == "view" {
				return []byte(`{"headRefOid":"abc","state":"OPEN","statusCheckRollup":[` +
					`{"__typename":"CheckRun","name":"test","workflowName":"ci","status":"COMPLETED","conclusion":"FAILURE","startedAt":"2026-03-01T10:00:00Z","completedAt":"2026-03-01T10:04:00Z","detailsUrl":"https://github.com/acme/widgets/actions/runs/7/job/21"},` +
					second + `]}`), nil
			}
			return fakeGH("abc", false)(ctx, a...)
		}
	}
	green := rollup(`{"__typename":"CheckRun","name":"test","workflowName":"ci","status":"COMPLETED","conclusion":"SUCCESS","startedAt":"2026-03-01T10:10:00Z","completedAt":"2026-03-01T10:14:00Z","detailsUrl":"https://github.com/acme/widgets/actions/runs/7/job/22"}`)
	going := rollup(`{"__typename":"CheckRun","name":"test","workflowName":"ci","status":"IN_PROGRESS","conclusion":"","startedAt":"2026-03-01T10:10:00Z","completedAt":"0001-01-01T00:00:00Z","detailsUrl":"https://github.com/acme/widgets/actions/runs/7/job/22"}`)
	for _, id := range []string{"ci-green", "head-current"} {
		if _, err := Verify(context.Background(), green, nil, id, testURL, "key", "step", nil); err != nil {
			t.Fatalf("%s refused a failure re-run green: %v", id, err)
		}
		if _, err := Verify(context.Background(), going, nil, id, testURL, "key", "step", nil); err == nil {
			t.Fatalf("%s passed while the latest run is in progress", id)
		}
	}
}

// GitHub leaves workflow_dispatch check suites (a manual staging deploy and its
// notify job) out of a commit's statusCheckRollup, though the commit's
// check-runs list them; gh pr checks reads that rollup and only dedupes. The
// fake answers like GitHub: the rollup holds the PR runs, check-runs also the
// failed deploy. ci-green and head-current must read the rollup (pass), and a
// failing PR check still refuses.
func TestVerifyCIIgnoresDispatchRunsLikeGHPRChecks(t *testing.T) {
	pr := func(conclusion string) string {
		return `{"__typename":"CheckRun","name":"test","workflowName":"ci","status":"COMPLETED","conclusion":"` + conclusion +
			`","startedAt":"2026-03-01T10:00:00Z","completedAt":"2026-03-01T10:04:00Z","detailsUrl":"https://github.com/acme/widgets/actions/runs/7/job/21"}`
	}
	github := func(prConclusion string) GHRunner {
		return func(ctx context.Context, a ...string) ([]byte, error) {
			if a[0] == "pr" && a[1] == "view" {
				return []byte(`{"headRefOid":"abc","state":"OPEN","statusCheckRollup":[` + pr(prConclusion) + `]}`), nil
			}
			if a[0] == "api" && strings.Contains(strings.Join(a, " "), "check-runs") {
				return []byte(`{"total_count":3,"check_runs":[` +
					`{"name":"test","status":"completed","conclusion":"` + strings.ToLower(prConclusion) + `"},` +
					`{"name":"Deploy staging","status":"completed","conclusion":"failure"},` +
					`{"name":"notify","status":"completed","conclusion":"failure"}]}`), nil
			}
			return fakeGH("abc", false)(ctx, a...)
		}
	}
	for _, id := range []string{"ci-green", "head-current"} {
		if _, err := Verify(context.Background(), github("SUCCESS"), nil, id, testURL, "key", "step", nil); err != nil {
			t.Fatalf("%s refused a green PR run beside a failed dispatch deploy: %v", id, err)
		}
		if _, err := Verify(context.Background(), github("FAILURE"), nil, id, testURL, "key", "step", nil); err == nil {
			t.Fatalf("%s passed a failing PR check", id)
		}
	}
}
