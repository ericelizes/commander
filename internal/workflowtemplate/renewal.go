package workflowtemplate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/example-user/commander/internal/reviewpolicy"
)

// A delta renewal carries a review of PriorHead to the current head without a
// new review, only when the verifier itself measures the fix delta on GitHub
// as tiny documentation: at most RenewalMaxFiles files and RenewalMaxLines
// changed lines, every file on the prose allow-list (proseFile),
// and reviewpolicy.Select reads it as focused (no sensitive path or wording).
// Anything else, or any read that fails, needs a new review of the delta.
const (
	RenewalMaxFiles = 3
	RenewalMaxLines = 20
)

// markupExt is prose anywhere outside agent configuration; .txt counts only
// under docs/ (requirements.txt, CMakeLists.txt are build input, not prose).
var markupExt = map[string]bool{".md": true, ".markdown": true, ".rst": true, ".adoc": true}

// agentDirs are path segments that hold agent instructions or tooling config.
var agentDirs = map[string]bool{"prompts": true, "agents": true, "skills": true, "commands": true, "hooks": true, "rules": true}

// agentFiles are instruction files read by agents wherever they sit.
var agentFiles = map[string]bool{"claude.md": true, "agents.md": true, "gemini.md": true, "skill.md": true, "copilot-instructions.md": true, "conventions.md": true}

// proseFile is an explicit allow-list: docs/** prose (markup or .txt) and
// markup files elsewhere, never inside a dot directory (.claude, .github,
// .cursor ...), an agent directory (prompts/, agents/, skills/ ...) or an agent
// instruction file (CLAUDE.md, AGENTS.md, GEMINI.md, SKILL.md ...). Build,
// config and agent files are behaviour, never prose.
func proseFile(p string) bool {
	low := strings.ToLower(strings.TrimPrefix(p, "./"))
	parts := strings.Split(low, "/")
	base := parts[len(parts)-1]
	if base == "" || agentFiles[base] || strings.HasPrefix(base, ".") {
		return false
	}
	for _, dir := range parts[:len(parts)-1] {
		if dir == "" || dir == ".." || strings.HasPrefix(dir, ".") || agentDirs[dir] {
			return false
		}
	}
	ext := path.Ext(base)
	if markupExt[ext] {
		return true
	}
	return ext == ".txt" && len(parts) > 1 && parts[0] == "docs"
}

type compareFile struct {
	Name      string `json:"filename"`
	Previous  string `json:"previous_filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch"`
}

// renewDelta measures prior..head through GitHub's compare API and says what it
// saw, or why the delta needs a new review.
func renewDelta(ctx context.Context, run GHRunner, pr, prior, head string) (string, error) {
	repo := strings.Split(strings.TrimPrefix(pr, "https://github.com/"), "/pull/")[0]
	out, err := run(ctx, "api", "repos/"+repo+"/compare/"+url.PathEscape(prior)+"..."+url.PathEscape(head), "--method", "GET",
		"--jq", `{status: .status, files: [.files[] | {filename, previous_filename, status, additions, deletions, patch}]}`)
	if err != nil {
		return "", fmt.Errorf("reading the fix delta %s..%s: %w", prior, head, err)
	}
	var c struct {
		Status string        `json:"status"`
		Files  []compareFile `json:"files"`
	}
	if err := json.Unmarshal(out, &c); err != nil {
		return "", fmt.Errorf("reading the fix delta %s..%s: %w", prior, head, err)
	}
	if c.Status != "ahead" {
		return "", fmt.Errorf("head %s does not build on the reviewed head %s (compare: %q): review the new head", head, prior, c.Status)
	}
	if len(c.Files) == 0 || len(c.Files) > RenewalMaxFiles {
		return "", fmt.Errorf("the fix delta touches %d files; a renewal covers 1 to %d documentation files", len(c.Files), RenewalMaxFiles)
	}
	var files []reviewpolicy.File
	var patch strings.Builder
	var names []string
	for _, f := range c.Files {
		for _, p := range []string{f.Name, f.Previous} {
			if p != "" && !proseFile(p) {
				return "", fmt.Errorf("the fix delta changes %s, which is not documentation: review the delta", p)
			}
		}
		// No patch is a binary or unreadable change, except a pure rename.
		if (f.Patch == "" && !(f.Status == "renamed" && f.Additions+f.Deletions == 0)) || f.Status == "removed" {
			return "", fmt.Errorf("the fix delta's change to %s cannot be read or removes it: review the delta", f.Name)
		}
		files = append(files, reviewpolicy.File{Path: f.Name, Added: f.Additions, Deleted: f.Deletions})
		fmt.Fprintf(&patch, "diff --git a/%s b/%s\n%s\n", f.Name, f.Name, f.Patch)
		names = append(names, f.Name)
	}
	sel := reviewpolicy.Select(files, patch.String(), false)
	if sel.Lines > RenewalMaxLines {
		return "", fmt.Errorf("the fix delta changes %d lines; a renewal covers at most %d", sel.Lines, RenewalMaxLines)
	}
	if sel.Depth != "focused" {
		return "", fmt.Errorf("the fix delta needs review: %s", sel.Reason)
	}
	return fmt.Sprintf("delta renewal of %s: %d documentation lines in %s, measured on GitHub", prior, sel.Lines, strings.Join(names, ", ")), nil
}
