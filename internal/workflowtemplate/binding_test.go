package workflowtemplate

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

func TestStepDoneDisposition(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Node, *Node)
		want bool
	}{
		{"done", func(n, b *Node) {}, true},
		{"no binding needed", func(n, b *Node) { b.Comments = nil }, true},
		{"checked on an older head", func(n, b *Node) {
			n.Comments = []beads.Comment{{Text: Evidence{Instance: Label(b.Bead, LabelInstance), Step: "devin", Verifier: "devin-comments-resolved", PR: testURL, Head: "old", Finding: "passed"}.Comment()}}
		}, true},
		{"no evidence", func(n, b *Node) { n.Comments = nil }, false},
		{"evidence for another PR", func(n, b *Node) {
			n.Comments = []beads.Comment{{Text: Evidence{Instance: Label(b.Bead, LabelInstance), Step: "devin", Verifier: "devin-comments-resolved", PR: "https://github.com/o/r/pull/9", Head: "abc", Finding: "passed"}.Comment()}}
		}, false},
		{"reopened", func(n, b *Node) { n.Bead.Status = "open" }, false},
		{"foreign parent", func(n, b *Node) { n.Bead.ParentID = "elsewhere" }, false},
		{"foreign instance", func(n, b *Node) { n.Bead.Labels = append([]string{LabelInstance + "elsewhere"}, n.Bead.Labels...) }, false},
		{"mutable owner irrelevant", func(n, b *Node) { n.Bead.Labels = append(n.Bead.Labels, "owner:manager-new") }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nodes := completeBlock(t)
			n, b := nodes[2], nodes[1]
			tc.edit(&n, &b)
			tpl, err := Definition(b.Bead)
			if err != nil {
				t.Fatal(err)
			}
			s := tpl.Steps[0]
			if got := StepDone(n, b, tpl, s.ID, s.Verifier, testURL); got != tc.want {
				t.Fatalf("done=%v want %v", got, tc.want)
			}
		})
	}
}

func TestExternalWriterBindingParse(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	for _, tc := range []struct {
		name string
		b    WriterBinding
		ok   bool
	}{
		{"user commits only", WriterBinding{Revision: "r", PR: "p", Head: "h", External: &ExternalWriter{Author: "user", Commits: []string{sha}}}, true},
		{"user commits and fix task", WriterBinding{Revision: "r", PR: "p", Head: "h", Tasks: []string{"fix"}, External: &ExternalWriter{Author: "user", Commits: []string{sha}}}, true},
		{"no writer at all", WriterBinding{Revision: "r", PR: "p", Head: "h"}, false},
		{"other author", WriterBinding{Revision: "r", PR: "p", Head: "h", External: &ExternalWriter{Author: "someone", Commits: []string{sha}}}, false},
		{"no commits", WriterBinding{Revision: "r", PR: "p", Head: "h", External: &ExternalWriter{Author: "user"}}, false},
		{"short sha", WriterBinding{Revision: "r", PR: "p", Head: "h", External: &ExternalWriter{Author: "user", Commits: []string{sha[:7]}}}, false},
		{"upper-case sha", WriterBinding{Revision: "r", PR: "p", Head: "h", External: &ExternalWriter{Author: "user", Commits: []string{"0123456789ABCDEF0123456789ABCDEF01234567"}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Binding([]beads.Comment{{Text: tc.b.Comment()}})
			if (err == nil) != tc.ok {
				t.Fatalf("ok=%v err=%v", tc.ok, err)
			}
			if !tc.ok && !strings.Contains(err.Error(), "--external-writer user") {
				t.Fatalf("hint missing: %v", err)
			}
			if tc.ok && (got.External == nil || got.External.Commits[0] != sha) {
				t.Fatal(got)
			}
		})
	}
}

func TestRangeCommitsFailsClosed(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	gh := func(status string, total int, list ...string) GHRunner {
		return func(_ context.Context, args ...string) ([]byte, error) {
			if slices.Contains(args, "--paginate") {
				return []byte(strings.Join(list, "\n") + "\n"), nil
			}
			return []byte(fmt.Sprintf("%s %d\n", status, total)), nil
		}
	}
	got, err := RangeCommits(context.Background(), gh("ahead", 2, a, b), "https://github.com/o/r/pull/7", "base", "tip")
	if err != nil || !slices.Equal(got, []string{a, b}) {
		t.Fatal(got, err)
	}
	for name, run := range map[string]GHRunner{
		"diverged":  gh("diverged", 2, a, b),
		"empty":     gh("ahead", 0),
		"short":     gh("ahead", 3, a, b),
		"bad sha":   gh("ahead", 1, "abc"),
		"gh failed": func(context.Context, ...string) ([]byte, error) { return nil, fmt.Errorf("boom") },
	} {
		if _, err := RangeCommits(context.Background(), run, "https://github.com/o/r/pull/7", "base", "tip"); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	for _, bad := range [][2]string{{"", "tip"}, {"base", "-x"}, {"a b", "tip"}, {"base", "tip^"}} {
		if _, err := RangeCommits(context.Background(), gh("ahead", 2, a, b), "https://github.com/o/r/pull/7", bad[0], bad[1]); err == nil {
			t.Fatalf("ref %q accepted", bad)
		}
	}
}

// A renewal keeps the writer kinds of the stored binding, never only --writers.
func TestRebindCommandKeepsTheWriterKinds(t *testing.T) {
	ext := &ExternalWriter{Author: "user", Commits: []string{strings.Repeat("1", 40)}}
	for _, tc := range []struct {
		name string
		old  *WriterBinding
		head string
		want string
	}{
		{"no binding", nil, "", "--block R --writers <original-task,actual-fix-task> --head <current-full-sha>"},
		{"tasks", &WriterBinding{Tasks: []string{"t"}}, "h", "--block R --writers <original-task,actual-fix-task> --head h"},
		{"external only", &WriterBinding{External: ext}, "", "--block R --external-writer user --commits <user-base-sha>..<user-last-sha> --head <current-full-sha>"},
		{"mixed", &WriterBinding{Tasks: []string{"t"}, External: ext}, "h", "--block R --writers <original-task,actual-fix-task> --external-writer user --commits <user-base-sha>..<user-last-sha> --head h"},
	} {
		if got := RebindCommand("p", "R", tc.old, tc.head); got != "cmdr workflow bind-writers --project p "+tc.want {
			t.Errorf("%s: %s", tc.name, got)
		}
	}
}
