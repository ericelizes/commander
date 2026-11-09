package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/workflowtemplate"
)

// workflowOperatorCommits says which of shas an Operator run of project wrote
// (sha -> why), task "" meaning every run of the project. An external writer is
// the user's own sessions, so a commit an Operator wrote is never bound as one,
// and a reviewer task that wrote a bound external commit is not independent.
var workflowOperatorCommits = func(ctx context.Context, project, task string, shas []string) (map[string]string, error) {
	st, done, err := needsYouStoreFn()
	if err != nil {
		return nil, err
	}
	defer done()
	p, err := st.GetProjectByName(ctx, project)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("unknown project %s", project)
	}
	var runs []store.AgentRun
	if task == "" {
		runs, err = st.ListAgentRunsByProject(ctx, p.ID, "")
	} else {
		runs, err = st.ListAgentRunsByTaskBeadID(ctx, task, "")
	}
	if err != nil {
		return nil, err
	}
	runs = slices.DeleteFunc(runs, func(r store.AgentRun) bool { return r.ProjectID != p.ID })
	repo := p.RepoPath
	if repo == "" {
		repo = p.Path
	}
	return operatorAuthorship(ctx, repo, p.Path, runs, shas)
}

// operatorAuthorship attributes shas to runs from the local repo, read only:
//   - reachable from a run's branch (its op/ name, its worktree's branch, or an
//     archived tip of it) and not from the commit the branch was created at
//     (the first entry of the branch's reflog; no reflog = all of it counts), so
//     a fix Operator branched off the user's commits does not claim them;
//   - made in a run's worktree (a commit, merge, cherry-pick, revert or rebase
//     step in its HEAD reflog);
//   - authored or committed under an identity only Operator runs used (seen in
//     their reflogs, never in the main checkout's reflog or its user.email).
//
// A commit missing from the local repo is on no local branch and passes. Any
// git failure is an error: the caller refuses.
func operatorAuthorship(ctx context.Context, repo, projectPath string, runs []store.AgentRun, shas []string) (map[string]string, error) {
	found := map[string]string{}
	if len(runs) == 0 || len(shas) == 0 {
		return found, nil
	}
	common, err := gitOut(ctx, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("cannot check Operator branches in %s: %w", repo, err)
	}
	for _, sha := range shas {
		if !workflowtemplate.FullSHA(sha) {
			return nil, fmt.Errorf("commit %q is not a full SHA", sha)
		}
	}
	// One read for all: "<sha> commit <size>" or "<sha> missing".
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "cat-file", "--batch-check")
	cmd.Stdin = strings.NewReader(strings.Join(shas, "\n") + "\n")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("cannot read commits in %s: %w", repo, err)
	}
	var present []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == "commit" && slices.Contains(shas, f[0]) {
			present = append(present, f[0])
		}
	}
	if len(present) == 0 {
		return found, nil
	}
	mark := func(sha, why string) {
		if _, ok := found[sha]; !ok {
			found[sha] = why
		}
	}
	userIDs := map[string]bool{}
	if email, err := gitOut(ctx, repo, "config", "user.email"); err == nil && email != "" {
		userIDs[strings.ToLower(email)] = true
	}
	for _, e := range readReflog(filepath.Join(common, "logs", "HEAD")) {
		userIDs[e.email] = true
	}
	opIDs := map[string]string{}
	branches := map[string]string{} // branch -> run that worked on it
	seenWT := map[string]bool{}
	for _, r := range runs {
		who := fmt.Sprintf("Operator run %s (task %s)", r.AgentName, r.TaskBeadID)
		if name, ok := strings.CutPrefix(strings.ToLower(r.AgentName), "operator-op-"); ok && name != "" {
			if _, ok := branches["op/"+name]; !ok {
				branches["op/"+name] = who
			}
		}
		wt := spawn.RunWorktree(projectPath, r)
		if seenWT[wt] {
			continue
		}
		seenWT[wt] = true
		gitdir := worktreeGitDir(wt)
		if gitdir == "" {
			continue
		}
		if head, err := os.ReadFile(filepath.Join(gitdir, "HEAD")); err == nil {
			if b, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: refs/heads/"); ok {
				if _, ok := branches[b]; !ok {
					branches[b] = who
				}
			}
		}
		for _, e := range readReflog(filepath.Join(gitdir, "logs", "HEAD")) {
			if _, ok := opIDs[e.email]; !ok {
				opIDs[e.email] = who
			}
			if authoredAction(e.msg) && slices.Contains(present, e.sha) {
				mark(e.sha, "made in the worktree of "+who)
			}
		}
	}
	for b, who := range branches {
		for _, e := range readReflog(filepath.Join(common, "logs", "refs", "heads", filepath.FromSlash(b))) {
			if _, ok := opIDs[e.email]; !ok {
				opIDs[e.email] = who
			}
		}
	}
	for _, sha := range present {
		out, err := gitOut(ctx, repo, "for-each-ref", "--contains", sha, "--format=%(refname)", "refs/heads/", "refs/archive/op/")
		if err != nil {
			return nil, fmt.Errorf("cannot list branches containing %s: %w", sha, err)
		}
		for _, ref := range strings.Fields(out) {
			b, who, base := refRun(ref, branches, common)
			if who == "" {
				continue
			}
			if base != "" {
				in, err := isAncestor(ctx, repo, sha, base)
				if err != nil {
					return nil, err
				}
				if in {
					continue // there before the Operator's branch was
				}
			}
			mark(sha, "on branch "+b+" of "+who)
		}
		ids, err := gitOut(ctx, repo, "show", "-s", "--format=%ae%n%ce", sha)
		if err != nil {
			return nil, fmt.Errorf("cannot read the identity of %s: %w", sha, err)
		}
		for _, id := range strings.Fields(strings.ToLower(ids)) {
			if who, ok := opIDs[id]; ok && !userIDs[id] {
				mark(sha, "under the identity "+id+" of "+who)
			}
		}
	}
	return found, nil
}

// refRun maps a ref containing a commit to the run branch it belongs to, with
// the commit that branch was created at ("" = unknown: count all of it).
func refRun(ref string, branches map[string]string, common string) (branch, who, base string) {
	if b, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		who = branches[b]
		if who == "" {
			return "", "", ""
		}
		if es := readReflog(filepath.Join(common, "logs", "refs", "heads", filepath.FromSlash(b))); len(es) > 0 {
			base = es[0].sha
		}
		return b, who, base
	}
	rest, ok := strings.CutPrefix(ref, "refs/archive/op/")
	if !ok {
		return "", "", ""
	}
	i := strings.LastIndex(rest, "-")
	if i <= 0 || !isHex(rest[i+1:]) {
		return "", "", ""
	}
	b := "op/" + rest[:i]
	return b + " (archived)", branches[b], ""
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

func isAncestor(ctx context.Context, repo, a, b string) (bool, error) {
	err := exec.CommandContext(ctx, "git", "-C", repo, "merge-base", "--is-ancestor", a, b).Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, nil
	}
	return false, fmt.Errorf("cannot compare %s with %s: %w", a, b, err)
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// worktreeGitDir is the admin dir of a linked worktree ("" when wt is none).
func worktreeGitDir(wt string) string {
	data, err := os.ReadFile(filepath.Join(wt, ".git"))
	if err != nil {
		return ""
	}
	dir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
	if !ok {
		return ""
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(wt, dir)
	}
	return dir
}

type reflogEntry struct{ sha, email, msg string }

// readReflog parses a reflog file oldest first ("old new Name <email> ts tz\tmsg").
func readReflog(path string) []reflogEntry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var es []reflogEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		head, msg, _ := strings.Cut(line, "\t")
		fields := strings.Fields(head)
		if len(fields) < 2 {
			continue
		}
		var email string
		if i, j := strings.Index(head, "<"), strings.LastIndex(head, ">"); i >= 0 && j > i {
			email = strings.ToLower(head[i+1 : j])
		}
		es = append(es, reflogEntry{sha: fields[1], email: email, msg: msg})
	}
	return es
}

// authoredAction reads a reflog message as one that wrote a new commit.
func authoredAction(msg string) bool {
	switch {
	case strings.HasPrefix(msg, "commit"), strings.HasPrefix(msg, "cherry-pick"), strings.HasPrefix(msg, "revert"):
		return true
	case strings.HasPrefix(msg, "merge "):
		return !strings.Contains(msg, "Fast-forward")
	case strings.HasPrefix(msg, "rebase"):
		for _, a := range []string{"(pick)", "(reword)", "(squash)", "(fixup)", "(edit)"} {
			if strings.Contains(msg, a) {
				return true
			}
		}
	}
	return false
}
