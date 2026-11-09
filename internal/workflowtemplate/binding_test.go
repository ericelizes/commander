package workflowtemplate

import (
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
