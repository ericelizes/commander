package workflowtemplate

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

func TestCheckCurrentBindingDisposition(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Node, *Node)
		want bool
	}{
		{"current", func(n, b *Node) {}, true},
		{"missing binding", func(n, b *Node) { b.Comments = nil }, false},
		{"malformed newest binding", func(n, b *Node) { b.Comments = append(b.Comments, beads.Comment{Text: BindingPrefix + "{"}) }, false},
		{"reopened", func(n, b *Node) { n.Bead.Status = "open" }, false},
		{"foreign parent", func(n, b *Node) { n.Bead.ParentID = "elsewhere" }, false},
		{"foreign instance", func(n, b *Node) { n.Bead.Labels = append([]string{LabelInstance + "elsewhere"}, n.Bead.Labels...) }, false},
		{"changed binding", func(n, b *Node) {
			binding, _ := Binding(b.Comments)
			binding.Revision = "rev2"
			b.Comments = append(b.Comments, beads.Comment{Text: binding.Comment()})
		}, false},
		{"reverted selection new revision", func(n, b *Node) {
			binding, _ := Binding(b.Comments)
			binding.Tasks = []string{"fix-task"}
			binding.Revision = "rev2"
			b.Comments = append(b.Comments, beads.Comment{Text: binding.Comment()})
			binding.Tasks = []string{"writer"}
			binding.Revision = "rev3"
			b.Comments = append(b.Comments, beads.Comment{Text: binding.Comment()})
		}, false},
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
			if got := CheckCurrent(n, b, tpl, s.ID, s.Verifier, testURL, "abc"); got != tc.want {
				t.Fatalf("current=%v want %v", got, tc.want)
			}
			for _, head := range []string{"", "unknown", "new"} {
				if CheckCurrent(n, b, tpl, s.ID, s.Verifier, testURL, head) {
					t.Fatalf("accepted head %q", head)
				}
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
