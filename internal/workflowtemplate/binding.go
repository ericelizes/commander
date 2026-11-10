package workflowtemplate

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/example-user/commander/internal/beads"
)

const BindingPrefix = "workflow-writers@v1 "

// ExternalAuthorUser is the only external writer: the user's own sessions,
// which have no task or Operator run. The bind refuses a commit an Operator
// run wrote, and a reviewer task whose runs wrote a bound commit is refused
// (cmd/cmdr workflowOperatorCommits).
const ExternalAuthorUser = "user"

// ExternalWriter records commits written outside any task, by their exact
// full SHAs (sorted), each checked to be on the PR when bound.
type ExternalWriter struct {
	Author  string   `json:"author"`
	Commits []string `json:"commits"`
}

// WriterBinding is an explicit Manager selection, never inferred from descendants.
// Revision changes whenever the selection or head changes, invalidating old checks.
type WriterBinding struct {
	Revision string          `json:"revision"`
	PR       string          `json:"pr"`
	Head     string          `json:"head"`
	Tasks    []string        `json:"tasks"`
	External *ExternalWriter `json:"external,omitempty"`
}

// FullSHA is a 40-character lower-case hex commit id.
func FullSHA(s string) bool {
	_, err := hex.DecodeString(s)
	return len(s) == 40 && err == nil && strings.ToLower(s) == s
}

func (e *ExternalWriter) valid() bool {
	if e == nil {
		return true
	}
	if e.Author != ExternalAuthorUser || len(e.Commits) == 0 {
		return false
	}
	for _, c := range e.Commits {
		if !FullSHA(c) {
			return false
		}
	}
	return true
}

const bindHint = "only the owning Manager can bind writers (Operators: mail your Manager); run cmdr workflow bind-writers --project <project> --block <block> --writers <original-task,fix-task> --head <current-full-sha> (for commits from the user's own sessions: --external-writer user --commits <base-sha>..<last-user-sha> [--writers <fix-task>])"

func (b WriterBinding) Comment() string {
	raw, _ := json.Marshal(b)
	return BindingPrefix + string(raw)
}
func Binding(cs []beads.Comment) (WriterBinding, error) {
	for i := len(cs) - 1; i >= 0; i-- {
		if raw, ok := strings.CutPrefix(cs[i].Text, BindingPrefix); ok {
			var b WriterBinding
			if json.Unmarshal([]byte(raw), &b) != nil || b.Revision == "" || b.PR == "" || b.Head == "" || (len(b.Tasks) == 0 && b.External == nil) || !b.External.valid() {
				return b, fmt.Errorf("invalid writer binding; %s", bindHint)
			}
			return b, nil
		}
	}
	return WriterBinding{}, fmt.Errorf("writer binding required; %s", bindHint)
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

// RangeCommits lists the full SHAs of from..to (reachable from to, not from
// from) through GitHub's compare API. It fails closed: to must be ahead of from,
// and the paged list must hold exactly the total GitHub counts.
func RangeCommits(ctx context.Context, run GHRunner, pr, from, to string) ([]string, error) {
	if run == nil {
		return nil, fmt.Errorf("GitHub runner unavailable")
	}
	if !validRef(from) || !validRef(to) {
		return nil, fmt.Errorf("commit range must be <base-sha>..<last-sha>")
	}
	repo := strings.Split(strings.TrimPrefix(pr, "https://github.com/"), "/pull/")[0]
	path := "repos/" + repo + "/compare/" + url.PathEscape(from) + "..." + url.PathEscape(to)
	out, err := run(ctx, "api", path, "--method", "GET", "--jq", `.status + " " + (.total_commits|tostring)`)
	if err != nil {
		return nil, err
	}
	var status string
	var total int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%s %d", &status, &total); err != nil {
		return nil, fmt.Errorf("GitHub compare returned %q", strings.TrimSpace(string(out)))
	}
	if status != "ahead" || total == 0 {
		return nil, fmt.Errorf("%s..%s is not a commit range ahead of its base (GitHub: %s, %d commits)", from, to, status, total)
	}
	out, err = run(ctx, "api", path+"?per_page=100", "--method", "GET", "--paginate", "--jq", ".commits[].sha")
	if err != nil {
		return nil, err
	}
	var shas []string
	for _, l := range strings.Fields(string(out)) {
		if !FullSHA(l) {
			return nil, fmt.Errorf("GitHub compare returned commit %q", l)
		}
		shas = append(shas, l)
	}
	if len(shas) != total {
		return nil, fmt.Errorf("GitHub compare listed %d of %d commits in %s..%s", len(shas), total, from, to)
	}
	return shas, nil
}

// validRef allows a SHA, or a branch/tag name with no range syntax.
func validRef(s string) bool {
	return s != "" && !strings.Contains(s, "..") && !strings.ContainsAny(s, " ~^:?*[\\") && !strings.HasPrefix(s, "-")
}

// RebindCommand is the bind-writers line that renews a block's binding: the
// writer kinds of the stored binding kept (task writers and/or the user's own
// commits), each as a placeholder to select again; head "" = a placeholder.
// With no readable binding it asks for task writers, as before.
func RebindCommand(project, block string, old *WriterBinding, head string) string {
	if head == "" {
		head = "<current-full-sha>"
	}
	cmd := fmt.Sprintf("cmdr workflow bind-writers --project %s --block %s", project, block)
	if old == nil || len(old.Tasks) > 0 || old.External == nil {
		cmd += " --writers <original-task,actual-fix-task>"
	}
	if old != nil && old.External != nil {
		cmd += " --external-writer user --commits <user-base-sha>..<user-last-sha>"
	}
	return cmd + " --head " + head
}
