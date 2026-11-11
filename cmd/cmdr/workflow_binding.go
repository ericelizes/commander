package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/workflowclose"
	"github.com/example-user/commander/internal/workflowtemplate"
)

// Resolve every historical role for this exact task, including completed runs.
// Multiple runs are expected; foreign-project or malformed rows are ambiguous.
var workflowTaskRoles = func(ctx context.Context, project, task string) ([]string, error) {
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
	runs, err := st.ListAgentRunsByTaskBeadID(ctx, task, "")
	if err != nil {
		return nil, err
	}
	var roles []string
	for _, r := range runs {
		if r.ProjectID != p.ID || r.TaskBeadID != task || strings.TrimSpace(r.AgentName) == "" {
			return nil, fmt.Errorf("ambiguous task run provenance for %s", task)
		}
		role, actorErr := workflowclose.Actor(r.AgentName)
		if actorErr != nil {
			return nil, fmt.Errorf("ambiguous execution role for task %s: %w", task, actorErr)
		}
		if !slices.Contains(roles, role) {
			roles = append(roles, role)
		}
	}
	if len(roles) == 0 {
		return nil, fmt.Errorf("task %s matches no Operator run", task)
	}
	return roles, nil
}

// Follow only fresh, explicit parent edges: promoted workstreams cannot be
// reconstructed from dotted IDs. Missing ancestry and cycles refuse the check.
// workflowOwnerRoles are the Managers that own id: the root's (from
// workflowRoot) while the project's nested Managers are off, else every owner
// over it, nearest first.
func workflowOwnerRoles(ctx context.Context, bc beads.Client, dir, id, root string) ([]string, error) {
	if !beads.NestedManagersOn(dir) {
		return []string{"manager-" + root}, nil
	}
	return ownerRoles(ctx, bc, dir, id)
}

func workflowRoot(ctx context.Context, bc beads.Client, dir, id string) (string, error) {
	return workflowVerifier().Root(ctx, bc, dir, id)
}
func workflowRolesInScope(ctx context.Context, bc beads.Client, dir, project, root, task string) ([]string, error) {
	other, err := workflowRoot(ctx, bc, dir, task)
	if err != nil {
		return nil, err
	}
	if other != root {
		return nil, fmt.Errorf("task %s is outside workstream %s", task, root)
	}
	return workflowTaskRoles(ctx, project, task)
}
func roleOverlap(a, b []string) bool {
	for _, r := range a {
		if slices.Contains(b, r) {
			return true
		}
	}
	return false
}

// externalWriterSpec asks to bind commits written outside any task: the user's
// own sessions. Commits is a <base>..<last> range or a comma list of full SHAs.
type externalWriterSpec struct {
	Author  string
	Commits string
}

func bindWorkflowWriters(ctx context.Context, bc beads.Client, dir, project, blockID, head string, tasks []string, external *externalWriterSpec) (*workflowtemplate.WriterBinding, error) {
	if callerIsOperator("") {
		return nil, fmt.Errorf("writer selection belongs to the owning Manager; Operators cannot bind writers")
	}
	bc = freshReader(bc)
	b, err := bc.Show(ctx, dir, blockID)
	if err != nil {
		return nil, err
	}
	if b == nil || workflowtemplate.Label(*b, workflowtemplate.LabelTemplate) != "pr-review" || workflowtemplate.Label(*b, workflowtemplate.LabelStep) != "" {
		return nil, fmt.Errorf("%s is not a pr-review block", blockID)
	}
	if _, err := workflowtemplate.Definition(*b); err != nil {
		return nil, err
	}
	root, err := workflowRoot(ctx, bc, dir, blockID)
	if err != nil {
		return nil, err
	}
	// Agents may bind only as an owning Manager (the block's nearest owner or one
	// above it; the root's alone while nested Managers are off). A human CLI has no role.
	// Commander may also bind the user's own commits as an external writer, never
	// task writers (those stay the owning Manager's choice).
	if role := os.Getenv("COMMANDER_ROLE"); role != "" && !(external != nil && len(tasks) == 0 && role == "commander") {
		owners, err := workflowOwnerRoles(ctx, bc, dir, blockID, root)
		if err != nil {
			return nil, err
		}
		if !roleAmong(role, owners) {
			return nil, fmt.Errorf("writer selection belongs to %s", owners[0])
		}
	}
	if external != nil && external.Author != workflowtemplate.ExternalAuthorUser {
		return nil, fmt.Errorf("--external-writer must be %q (the user's own sessions)", workflowtemplate.ExternalAuthorUser)
	}
	if external != nil && strings.TrimSpace(external.Commits) == "" {
		return nil, fmt.Errorf("--external-writer needs --commits <base-sha>..<last-sha> or a comma list of full SHAs")
	}
	if len(tasks) == 0 && external == nil {
		return nil, fmt.Errorf("explicit original and actual fix writer tasks required")
	}
	slices.Sort(tasks)
	tasks = slices.Compact(tasks)
	if tasks == nil {
		tasks = []string{}
	}
	for _, task := range tasks {
		if task == "" {
			return nil, fmt.Errorf("empty writer task")
		}
		if _, err := workflowRolesInScope(ctx, bc, dir, project, root, task); err != nil {
			return nil, err
		}
	}
	pr := workflowtemplate.BlockPR(*b)
	h, err := workflowtemplate.ReadHead(ctx, workflowGH, pr)
	if err != nil {
		return nil, err
	}
	_, headErr := hex.DecodeString(head)
	if h.SHA != head || len(head) != 40 || headErr != nil {
		return nil, fmt.Errorf("--head must equal current full PR head %s", h.SHA)
	}
	if h.State != "OPEN" {
		return nil, fmt.Errorf("writer binding requires an open PR")
	}
	var ext *workflowtemplate.ExternalWriter
	if external != nil {
		commits, err := externalCommits(ctx, pr, h, external.Commits)
		if err != nil {
			return nil, err
		}
		// The user's own sessions only: a commit an Operator run wrote is that run's task's work.
		wrote, err := workflowOperatorCommits(ctx, project, "", commits)
		if err != nil {
			return nil, fmt.Errorf("cannot check the external commits against Operator runs: %w", err)
		}
		for _, sha := range commits {
			if why, ok := wrote[sha]; ok {
				return nil, fmt.Errorf("commit %s is not the user's own: %s; bind that task with --writers instead", sha, why)
			}
		}
		ext = &workflowtemplate.ExternalWriter{Author: external.Author, Commits: commits}
	}
	cs, err := bc.Comments(ctx, dir, blockID)
	if err != nil {
		return nil, err
	}
	old, err := workflowtemplate.Binding(cs)
	// Commander records only the user's commits: the task writers the owning
	// Manager chose for this PR stay bound.
	if err == nil && old.PR == pr && os.Getenv("COMMANDER_ROLE") == "commander" && len(tasks) == 0 {
		tasks = slices.Clone(old.Tasks)
	}
	if err == nil && old.PR == pr && old.Head == head && slices.Equal(old.Tasks, tasks) && sameExternal(old.External, ext) {
		return &old, nil
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	binding := workflowtemplate.WriterBinding{Revision: hex.EncodeToString(nonce), PR: pr, Head: head, Tasks: tasks, External: ext}
	// Check head again before persisting the selection.
	h, err = workflowtemplate.ReadHead(ctx, workflowGH, pr)
	if err != nil {
		return nil, err
	}
	if h.SHA != head {
		return nil, fmt.Errorf("PR head changed while binding; retry")
	}
	if err := bc.AddComment(ctx, dir, blockID, binding.Comment()); err != nil {
		return nil, err
	}
	return &binding, nil
}

// externalCommits resolves --commits to the exact sorted full SHAs, each one
// a commit of the PR at head h. Short SHAs and refs in a list are refused: the
// record names exact commits.
func externalCommits(ctx context.Context, pr string, h workflowtemplate.Head, spec string) ([]string, error) {
	var shas []string
	if from, to, ok := strings.Cut(spec, ".."); ok {
		if strings.HasPrefix(to, ".") || strings.Contains(spec, ",") {
			return nil, fmt.Errorf("--commits takes one <base>..<last> range (two dots) or a comma list of full SHAs")
		}
		var err error
		if shas, err = workflowtemplate.RangeCommits(ctx, workflowGH, pr, strings.TrimSpace(from), strings.TrimSpace(to)); err != nil {
			return nil, err
		}
	} else {
		for _, c := range strings.Split(spec, ",") {
			c = strings.TrimSpace(c)
			if !workflowtemplate.FullSHA(c) {
				return nil, fmt.Errorf("--commits %q is not a full 40-character SHA", c)
			}
			shas = append(shas, c)
		}
	}
	slices.Sort(shas)
	shas = slices.Compact(shas)
	if len(shas) == 0 {
		return nil, fmt.Errorf("--commits names no commit")
	}
	for _, sha := range shas {
		on, err := workflowtemplate.OnPR(ctx, workflowGH, pr, h, sha)
		if err != nil {
			return nil, fmt.Errorf("commit %s: %w", sha, err)
		}
		if !on {
			return nil, fmt.Errorf("commit %s is not on the PR", sha)
		}
	}
	return shas, nil
}

func sameExternal(a, b *workflowtemplate.ExternalWriter) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Author == b.Author && slices.Equal(a.Commits, b.Commits)
}

func runWorkflowBindWriters(args []string, stdout, stderr *os.File) int {
	if callerIsOperator("") {
		emitErr(stderr, "writer selection belongs to the owning Manager; Operators cannot bind writers")
		return 1
	}
	fs := flag.NewFlagSet("workflow bind-writers", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "project slug")
	block := fs.String("block", "", "existing review block")
	writers := fs.String("writers", "", "comma-separated original and actual fix task IDs, selected from commit reports")
	head := fs.String("head", "", "current full PR head")
	externalWriter := fs.String("external-writer", "", "user: commits from the user's own sessions (no task); needs --commits")
	commits := fs.String("commits", "", "with --external-writer: <base-sha>..<last-sha> or a comma list of full SHAs on the PR")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *project == "" || *block == "" || (*writers == "" && *externalWriter == "") || *head == "" || fs.NArg() != 0 || (*commits != "") != (*externalWriter != "") {
		emitErr(stderr, "required: --project P --block B --head full-sha and --writers original-task,fix-task and/or --external-writer user --commits base-sha..last-sha")
		return 1
	}
	var external *externalWriterSpec
	if *externalWriter != "" {
		external = &externalWriterSpec{Author: *externalWriter, Commits: *commits}
	}
	var tasks []string
	if *writers != "" {
		tasks = strings.Split(*writers, ",")
	}
	p, err := resolveProjectRecFn(*project)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	unlock := lockPlug("writers-" + *block)
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	b, err := bindWorkflowWriters(ctx, newBeadsClientFn(), p.Path, p.Name, *block, *head, tasks, external)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(b); err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	return 0
}
