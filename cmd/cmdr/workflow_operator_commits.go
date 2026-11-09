package main

import (
	"context"
	"fmt"
	"os/exec"
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
	return operatorAuthorship(ctx, repo, runs, task == "", shas)
}

// operatorAuthorship attributes shas by the identity in each commit object,
// read only from the local repo. The launcher gives every Operator run its own
// committer identity (spawn.OperatorGitEmail), and git writes the committer of
// every commit object a process makes (commit, amend, cherry-pick, revert, am,
// merge, rebase), while a ref move (reset, fast-forward, checkout, branch -f)
// makes no object. So a commit whose author or committer is an Operator
// identity was written by that Operator, and a reviewer that reset onto the
// user's commits wrote none of them. No reflog and no commit message is read:
// commit text cannot change the answer.
//
// anyOperator (the project-wide read at bind) counts every identity under
// spawn.OperatorGitDomain, a run of this project or not; otherwise only the
// identities of runs count. A commit missing from the local repo passes. Any
// git failure is an error: the caller refuses.
//
// Limit: an Operator started before its launcher set the identity, or one that
// unsets it, commits as the configured user and is not attributed.
func operatorAuthorship(ctx context.Context, repo string, runs []store.AgentRun, anyOperator bool, shas []string) (map[string]string, error) {
	found := map[string]string{}
	if len(shas) == 0 || (len(runs) == 0 && !anyOperator) {
		return found, nil
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
		if len(f) >= 2 && f[1] == "commit" && slices.Contains(shas, f[0]) && !slices.Contains(present, f[0]) {
			present = append(present, f[0])
		}
	}
	if len(present) == 0 {
		return found, nil
	}
	who := map[string]string{} // identity -> run
	for _, r := range runs {
		id := spawn.OperatorGitEmail(r.AgentName)
		if _, ok := who[id]; !ok {
			who[id] = fmt.Sprintf("Operator run %s (task %s)", r.AgentName, r.TaskBeadID)
		}
	}
	// %ae/%ce are the raw headers (no mailmap); %x09 cannot occur in either.
	ids, err := gitOut(ctx, repo, append([]string{"show", "-s", "--no-show-signature", "--format=%H%x09%ae%x09%ce"}, present...)...)
	if err != nil {
		return nil, fmt.Errorf("cannot read the identities of the commits in %s: %w", repo, err)
	}
	read := 0
	for _, line := range strings.Split(ids, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 || !slices.Contains(present, f[0]) {
			continue
		}
		read++
		for _, id := range []string{strings.ToLower(f[2]), strings.ToLower(f[1])} {
			if run, ok := who[id]; ok {
				found[f[0]] = "committed as " + id + " by " + run
				break
			}
			if anyOperator && strings.HasSuffix(id, "@"+spawn.OperatorGitDomain) {
				found[f[0]] = "committed as " + id + ", an Operator identity"
				break
			}
		}
	}
	if read != len(present) {
		return nil, fmt.Errorf("read the identities of %d of %d commits in %s", read, len(present), repo)
	}
	return found, nil
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}
