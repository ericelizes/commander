// Package reviewpolicy selects bounded review coverage from committed Git scope.
package reviewpolicy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const Version = "review-policy@v1"

type File struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	Binary  bool   `json:"binary,omitempty"`
}

type Result struct {
	Version     string `json:"version"`
	Base        string `json:"base"`
	Head        string `json:"head"`
	ScopeHash   string `json:"scope_hash"`
	Depth       string `json:"depth"`
	Reason      string `json:"reason"`
	Files       []File `json:"files"`
	Lines       int    `json:"changed_lines"`
	Adversarial string `json:"adversarial"`
}

// Inspect resolves refs before examining the committed diff; it reads no home,
// credential files, working-tree contents, external diff drivers or textconv.
func Inspect(ctx context.Context, dir, base, head string, full bool) (Result, error) {
	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "--no-pager"}, args...)...)
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			if !strings.HasPrefix(key, "GIT_") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1")
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("git scope inspection failed: %w", err)
		}
		return out, nil
	}
	resolve := func(ref string) (string, error) {
		if ref == "" || strings.HasPrefix(ref, "-") {
			return "", fmt.Errorf("base and head must be commit refs")
		}
		b, err := run("rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
		return strings.TrimSpace(string(b)), err
	}
	b, err := resolve(base)
	if err != nil {
		return Result{}, err
	}
	h, err := resolve(head)
	if err != nil {
		return Result{}, err
	}
	// No rename inference: both old and new sensitive paths must be classified.
	stats, err := run("diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--numstat", "-z", b, h, "--")
	if err != nil {
		return Result{}, err
	}
	patch, err := run("diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--unified=0", b, h, "--")
	if err != nil {
		return Result{}, err
	}
	files := []File{}
	for _, s := range strings.Split(string(stats), "\x00") {
		if s == "" {
			continue
		}
		parts := strings.SplitN(s, "\t", 3)
		if len(parts) != 3 {
			return Result{}, fmt.Errorf("invalid git scope statistics")
		}
		f := File{Path: parts[2], Binary: parts[0] == "-" || parts[1] == "-"}
		if !f.Binary {
			f.Added, err = strconv.Atoi(parts[0])
			if err != nil {
				return Result{}, fmt.Errorf("invalid added count")
			}
			f.Deleted, err = strconv.Atoi(parts[1])
			if err != nil {
				return Result{}, fmt.Errorf("invalid deleted count")
			}
		}
		files = append(files, f)
	}
	// Patch content stays in memory and only its digest leaves this package.
	r := Select(files, string(patch), full)
	r.Base = b
	r.Head = h
	digest := sha256.Sum256([]byte(Version + "\x00" + b + "\x00" + h + "\x00" + string(stats) + "\x00" + string(patch)))
	r.ScopeHash = hex.EncodeToString(digest[:])
	return r, nil
}

var sensitivePath = regexp.MustCompile(`(?i)(^|[/_.-])(auth[a-z]*|security|permission[a-z]*|credential[a-z]*|secret[a-z]*|token[a-z]*|crypto[a-z]*|encrypt[a-z]*|session[a-z]*|migration[a-z]*|data|db|schema|sql|database|storage|access|acl|rbac|oauth|sso|egress|runtime|process|shutdown|backup[a-z]*|restore[a-z]*|lifecycle|cancel[a-z]*|billing|payment[a-z]*|infra[a-z]*|terraform|ansible|deploy[a-z]*|release[a-z]*|docker[a-z]*|kubernetes|helm|iam|policy|guard[a-z]*)([/_.-]|$)`)
var sensitiveContent = regexp.MustCompile(`(?i)\b(auth[a-z]*|permission[a-z]*|credential[a-z]*|secret[a-z]*|token[a-z]*|encrypt[a-z]*|decrypt[a-z]*|password[a-z]*|sudo|chmod|chown|eval|exec|drop\s+table|truncate|delete\s+from|rm\s+-[a-z]*r|force.with.lease|cancel[a-z]*|rollback|restore|backup|persist[a-z]*|transaction[a-z]*|destroy[a-z]*|removeall|unlink|delete|insert|update|writefile|kill|terminate)\b`)
var ciBudget = regexp.MustCompile(`(timeout-minutes:\s*|--timeout(?:=|\s+))[0-9]+`)

// Select is conservative on sensitive domains, binary and broad changes even
// when the line count is tiny. The independent challenge never repeats a roster.
func Select(files []File, patch string, full bool) Result {
	files = append([]File{}, files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	r := Result{Version: Version, Depth: "focused", Reason: "small scope without a sensitive risk signal", Files: files, Adversarial: "independent bounded challenge; no second primary roster"}
	changed := []string{}
	for _, line := range strings.Split(patch, "\n") {
		if (strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++")) || (strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---")) {
			changed = append(changed, line[1:])
		}
	}
	for _, f := range files {
		r.Lines += f.Added + f.Deleted
	}
	reason := ""
	for _, f := range files {
		path := strings.ToLower(f.Path)
		if f.Binary {
			reason = "binary change requires full review"
			break
		}
		if sensitivePath.MatchString(path) {
			reason = "sensitive risk domain: " + f.Path
			break
		}
		if strings.HasPrefix(path, ".github/") || strings.HasSuffix(path, ".tf") || strings.HasSuffix(path, ".tfvars") {
			if !onlyCIBudget(patch, f.Path) {
				reason = "infrastructure or CI behavior: " + f.Path
				break
			}
		}
	}
	if reason == "" && sensitiveContent.MatchString(strings.Join(changed, "\n")) {
		reason = "sensitive behavior in changed lines"
	}
	if reason == "" && (len(files) > 6 || r.Lines > 200) {
		reason = "broad scope exceeds focused budget (6 files / 200 lines)"
	}
	if reason == "" && len(files) == 0 {
		reason = "empty committed scope; no review coverage to claim"
	}
	if full {
		reason = "explicit full review override"
	}
	if reason != "" {
		r.Depth = "full"
		r.Reason = reason
	}
	return r
}

// Only an existing budget number may change: command names, flags, permissions
// and all other bytes on that line must remain identical.
func onlyCIBudget(patch, path string) bool {
	active := false
	before, after := []string{}, []string{}
	normalize := func(line string) string { return ciBudget.ReplaceAllString(line, "${1}<budget>") }
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			active = line == "diff --git a/"+path+" b/"+path
			continue
		}
		if !active || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") {
			continue
		}
		if strings.HasPrefix(line, "-") {
			before = append(before, line[1:])
		}
		if strings.HasPrefix(line, "+") {
			after = append(after, line[1:])
		}
	}
	if len(before) == 0 || len(before) != len(after) {
		return false
	}
	for i, line := range before {
		if !ciBudget.MatchString(line) || !ciBudget.MatchString(after[i]) || normalize(line) != normalize(after[i]) {
			return false
		}
	}
	return true
}
