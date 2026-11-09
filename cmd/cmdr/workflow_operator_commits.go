package main

import (
	"bufio"
	"context"
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
//   - written on a run's branch (its op/ name or its worktree's branch): an
//     entry of the branch's reflog that made a commit (commit, non-fast-forward
//     merge, cherry-pick, revert, am, a rebase's new commits), never one that
//     only moved the branch (created from, reset, checkout, fast-forward), so a
//     reviewer that reset onto the PR head or a fix Operator branched off the
//     user's commits does not claim them; a branch with no reflog counts all of
//     it, as does an archived tip of one (refs/archive/op/; nothing tells there);
//   - made in a run's worktree (the same entries in its HEAD reflog);
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
		es := readReflog(filepath.Join(gitdir, "logs", "HEAD"))
		if err := claimWritten(ctx, repo, es, present, "made in the worktree of "+who, mark); err != nil {
			return nil, err
		}
		for _, e := range es {
			if _, ok := opIDs[e.email]; !ok {
				opIDs[e.email] = who
			}
		}
	}
	noReflog := map[string]bool{}
	for b, who := range branches {
		es := readReflog(filepath.Join(common, "logs", "refs", "heads", filepath.FromSlash(b)))
		if len(es) == 0 {
			noReflog[b] = true
		}
		if err := claimWritten(ctx, repo, es, present, "on branch "+b+" of "+who, mark); err != nil {
			return nil, err
		}
		for _, e := range es {
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
			if b, who := refRun(ref, branches, noReflog); who != "" {
				mark(sha, "on branch "+b+" of "+who)
			}
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

// refRun maps a ref containing a commit to the run whose whole history it
// counts as written: an archived tip, or a run branch with no reflog.
func refRun(ref string, branches map[string]string, noReflog map[string]bool) (branch, who string) {
	if b, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		if !noReflog[b] {
			return "", ""
		}
		return b, branches[b]
	}
	rest, ok := strings.CutPrefix(ref, "refs/archive/op/")
	if !ok {
		return "", ""
	}
	i := strings.LastIndex(rest, "-")
	if i <= 0 || !isHex(rest[i+1:]) {
		return "", ""
	}
	b := "op/" + rest[:i]
	return b + " (archived)", branches[b]
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

type reflogEntry struct{ old, sha, email, msg string }

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
		es = append(es, reflogEntry{old: fields[0], sha: fields[1], email: email, msg: msg})
	}
	return es
}

// claimWritten marks each of present that an entry of es wrote (why).
func claimWritten(ctx context.Context, repo string, es []reflogEntry, present []string, why string, mark func(sha, why string)) error {
	for _, e := range es {
		switch c, onto := reflogWrote(e.msg); c {
		case wroteNew:
			if slices.Contains(present, e.sha) {
				mark(e.sha, why)
			}
		case wroteRange:
			// A finished rebase: the commits it made are on its new tip and on
			// neither its old tip nor what it was rebased onto.
			args := []string{"rev-list", e.sha, "^" + onto}
			if strings.Trim(e.old, "0") != "" {
				args = append(args, "^"+e.old)
			}
			out, err := gitOut(ctx, repo, args...)
			if err != nil {
				return fmt.Errorf("cannot read the commits of %q: %w", e.msg, err)
			}
			for _, sha := range strings.Fields(out) {
				if slices.Contains(present, sha) {
					mark(sha, why)
				}
			}
		}
	}
	return nil
}

const (
	wroteNothing = iota
	wroteNew     // the entry's new value is a commit it made
	wroteRange   // a finished rebase onto a commit
)

// reflogWrote reads a reflog message: did that update make commits? Moves that
// only point the ref somewhere (created from, reset, checkout, any
// fast-forward, a rebase's start) wrote nothing.
func reflogWrote(msg string) (kind int, onto string) {
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "fast-forward") {
		return wroteNothing, ""
	}
	if strings.HasPrefix(msg, "rebase") || strings.HasPrefix(msg, "pull --rebase") {
		for _, a := range []string{"(pick)", "(reword)", "(squash)", "(fixup)", "(edit)"} {
			if strings.Contains(msg, a) {
				return wroteNew, ""
			}
		}
		if strings.Contains(msg, "(finish)") {
			if _, o, ok := strings.Cut(msg, " onto "); ok && isHex(strings.TrimSpace(o)) {
				return wroteRange, strings.TrimSpace(o)
			}
		}
		return wroteNothing, ""
	}
	for _, p := range []string{"commit", "cherry-pick", "revert", "am:", "merge ", "pull:", "pull "} {
		if strings.HasPrefix(msg, p) {
			return wroteNew, ""
		}
	}
	return wroteNothing, ""
}
