package workflowtemplate

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/example-user/commander/internal/beads"
)

const BindingPrefix = "workflow-writers@v1 "

// WriterBinding is an explicit Manager selection, never inferred from descendants.
// Revision changes whenever the selection or head changes, invalidating old checks.
type WriterBinding struct {
	Revision string   `json:"revision"`
	PR       string   `json:"pr"`
	Head     string   `json:"head"`
	Tasks    []string `json:"tasks"`
}

func (b WriterBinding) Comment() string {
	raw, _ := json.Marshal(b)
	return BindingPrefix + string(raw)
}
func Binding(cs []beads.Comment) (WriterBinding, error) {
	for i := len(cs) - 1; i >= 0; i-- {
		if raw, ok := strings.CutPrefix(cs[i].Text, BindingPrefix); ok {
			var b WriterBinding
			if json.Unmarshal([]byte(raw), &b) != nil || b.Revision == "" || b.PR == "" || b.Head == "" || len(b.Tasks) == 0 {
				return b, fmt.Errorf("invalid writer binding; only the owning Manager can bind writers (Operators: mail your Manager); run cmdr workflow bind-writers --project <project> --block <block> --writers <original-task,fix-task> --head <current-full-sha>")
			}
			return b, nil
		}
	}
	return WriterBinding{}, fmt.Errorf("writer binding required; only the owning Manager can bind writers (Operators: mail your Manager); run cmdr workflow bind-writers --project <project> --block <block> --writers <original-task,fix-task> --head <current-full-sha>")
}
func BoundEvidenceFor(n Node, block Node, instance, step, verifier, pr, head string) bool {
	b, err := Binding(block.Comments)
	if err != nil || b.PR != pr || b.Head != head {
		return false
	}
	for _, c := range n.Comments {
		e, ok := ParseEvidence(c.Text)
		if ok && e.Binding == b.Revision && e.Instance == instance && e.Step == step && e.Verifier == verifier && e.PR == pr && e.Head == head && head != "" {
			return true
		}
	}
	return false
}

// CheckCurrent is the exact per-step disposition shared by the rule and display.
// Template identity comes from the validated snapshot, never from its evidence.
func CheckCurrent(n, block Node, t *Template, step, verifier, pr, head string) bool {
	return n.Bead.Status == "closed" && n.Bead.ParentID == block.Bead.ID &&
		Label(n.Bead, LabelInstance) == Label(block.Bead, LabelInstance) &&
		Label(n.Bead, LabelHash) == t.Hash() &&
		BoundEvidenceFor(n, block, Label(block.Bead, LabelInstance), step, verifier, pr, head)
}
