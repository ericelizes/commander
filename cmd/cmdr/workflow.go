package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/workflowtemplate"
)

type workflowStepOut struct {
	Step   string `json:"step"`
	BeadID string `json:"bead_id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type workflowPlugResult struct {
	Project   string            `json:"project"`
	Template  string            `json:"template"`
	Version   int               `json:"version"`
	Instance  string            `json:"instance"`
	Container string            `json:"container"`
	Before    string            `json:"before"`
	PR        string            `json:"pr"`
	Created   []string          `json:"created,omitempty"`
	Remaining []workflowStepOut `json:"remaining"`
	Note      string            `json:"note,omitempty"`
}

// runWorkflow: `workflow plug <template> --project P --parent B --before M --pr URL`.
func runWorkflow(args []string, stdout, stderr *os.File) int {
	if len(args) > 0 && args[0] == "review-exec" {
		return runWorkflowReviewExec(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "review-policy" {
		return runWorkflowReviewPolicy(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "add-review-addendum" {
		return runWorkflowAddendum(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "review-report-check" {
		return runWorkflowReviewReportCheck(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "bind-writers" {
		return runWorkflowBindWriters(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "settle-block" {
		return runWorkflowSettleBlock(args[1:], stdout, stderr)
	}
	if len(args) == 0 || args[0] != "plug" {
		emitErr(stderr, "usage: workflow plug <template> --project P --parent B --before M --pr URL | workflow bind-writers --project P --block B --writers original-task,fix-task --head full-sha | workflow settle-block --project P --block B --reason R")
		return 1
	}
	return runWorkflowPlug(args[1:], stdout, stderr)
}

var prURLRe = regexp.MustCompile(`^/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)/pull/([0-9]+)$`)

// canonicalPR reduces a GitHub PR URL to https://github.com/owner/repo/pull/N.
func canonicalPR(raw string) (string, error) {
	return workflowtemplate.CanonicalPR(raw)
}

func runWorkflowPlug(args []string, stdout, stderr *os.File) int {
	// The template name comes first, flags after.
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		emitErr(stderr, "usage: workflow plug <template> --project P --parent B --before M --pr URL | workflow bind-writers --project P --block B --writers original-task,fix-task --head full-sha | workflow settle-block --project P --block B --reason R")
		return 1
	}
	name, args := args[0], args[1:]
	fs := flag.NewFlagSet("workflow plug", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	projectName := fs.String("project", "", "project slug")
	parent := fs.String("parent", "", "the bead the Review gate block goes under")
	before := fs.String("before", "", "the bead that must wait on the block (the merge step)")
	pr := fs.String("pr", "", "the PR URL the review is about")
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	if *projectName == "" || *parent == "" || *before == "" || *pr == "" {
		emitErr(stderr, "missing required flag(s): --project, --parent, --before, --pr")
		return 1
	}
	prURL, err := canonicalPR(*pr)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	tpl, err := workflowtemplate.Load(name)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	project, err := resolveProject(*projectName)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	inst, err := workflowtemplate.Instantiate(tpl, workflowtemplate.Params{Project: project.Name, Parent: *parent, Before: *before, PR: prURL})
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	unlock := lockPlug(inst.Key)
	defer unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	// A block for a merged PR would exempt its target, whatever that target really
	// ships. An unreadable PR does not refuse: plug itself needs no GitHub.
	if h, err := workflowtemplate.ReadHead(ctx, workflowGH, prURL); err == nil && h.State == "MERGED" {
		emitErr(stderr, fmt.Sprintf("%s is already merged: there is nothing left to review; nothing written", prURL))
		return 1
	}
	res, err := plugWorkflow(ctx, newBeadsClientFn(), project.Path, inst)
	if res != nil {
		if encErr := json.NewEncoder(stdout).Encode(res); encErr != nil && err == nil {
			err = encErr
		}
	}
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	return 0
}

// lockPlug serializes plugs of one instance across processes. Best effort: if
// the lock file cannot be taken the plug still runs (it is resumable anyway).
func lockPlug(key string) func() {
	home := os.Getenv("COMMANDER_HOME")
	if home == "" {
		return func() {}
	}
	dir := filepath.Join(home, "workflow-locks")
	if os.MkdirAll(dir, 0o700) != nil {
		return func() {}
	}
	f, err := os.OpenFile(filepath.Join(dir, key+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX) != nil {
		f.Close()
		return func() {}
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }
}

func labelWithPrefix(b beads.Bead, prefix string) string {
	for _, x := range b.Labels {
		if v, ok := strings.CutPrefix(x, prefix); ok {
			return v
		}
	}
	return ""
}

// plugWorkflow makes (or completes) the block for inst. Everything that can be
// refused is checked before the first write; every write is skipped when its
// result already exists, so a rerun after a partial failure finishes the job.
// It never closes or passes anything.
func plugWorkflow(ctx context.Context, bc beads.Client, dir string, inst *workflowtemplate.Instance) (*workflowPlugResult, error) {
	p := inst.Params
	if p.Parent == p.Before {
		return nil, fmt.Errorf("--parent and --before are the same bead: the block would sit under the bead that waits on it")
	}
	target, err := bc.Show(ctx, dir, p.Before)
	if err != nil || target == nil {
		return nil, fmt.Errorf("could not read --before %s (%v); nothing written", p.Before, err)
	}
	if target.Status == "closed" {
		return nil, fmt.Errorf("--before %s is already closed: there is nothing left to put a review in front of", p.Before)
	}
	par, err := bc.Show(ctx, dir, p.Parent)
	if err != nil || par == nil {
		return nil, fmt.Errorf("could not read --parent %s (%v); nothing written", p.Parent, err)
	}
	if par.Status == "closed" {
		return nil, fmt.Errorf("--parent %s is closed", p.Parent)
	}

	existing, err := bc.List(ctx, dir, beads.ListOptions{Labels: []string{workflowtemplate.LabelInstance + inst.Key}, Status: "all"})
	if err != nil {
		return nil, fmt.Errorf("could not look for an existing %s block (%v); nothing written", inst.Template, err)
	}
	var container *beads.Bead
	steps := map[string]beads.Bead{}
	for _, b := range existing {
		b := b
		if s := labelWithPrefix(b, workflowtemplate.LabelStep); s != "" {
			if _, dup := steps[s]; dup {
				return nil, fmt.Errorf("two beads carry step %s of instance %s (%s, %s): refusing to guess, mail your Manager", s, inst.Key, steps[s].ID, b.ID)
			}
			steps[s] = b
		} else {
			if container != nil {
				return nil, fmt.Errorf("two beads are the block of instance %s (%s, %s): refusing to guess, mail your Manager", inst.Key, container.ID, b.ID)
			}
			container = &b
		}
	}
	if container != nil {
		if v := labelWithPrefix(*container, workflowtemplate.LabelVersion); v != fmt.Sprint(inst.Version) {
			return nil, fmt.Errorf("%s is an instance of %s version %s, this build has version %s: refusing to change a running block", container.ID, inst.Template, v, fmt.Sprint(inst.Version))
		}
	}

	// A different PR plugged before the same target is a conflict, not a second block.
	deps, err := bc.Deps(ctx, dir, p.Before)
	if err != nil {
		return nil, fmt.Errorf("could not read what %s waits on (%v); nothing written", p.Before, err)
	}
	for _, d := range deps {
		if d.Type != "blocks" {
			continue
		}
		if o, err := bc.Show(ctx, dir, d.DependsOnID); err == nil && o != nil &&
			hasLabel(o.Labels, workflowtemplate.LabelTemplate+inst.Template) && labelWithPrefix(*o, workflowtemplate.LabelInstance) != inst.Key {
			return nil, fmt.Errorf("%s already waits on %s, a %s block for another PR or parent: refusing a second block (one review gate per merge)", p.Before, o.ID, inst.Template)
		}
	}
	// Cycle: the block hangs under --parent; if --parent already waits on the
	// target the target could never start.
	if dependsOn(ctx, bc, dir, p.Parent, p.Before) {
		return nil, fmt.Errorf("%s already waits on %s, so a block under %s that %s waits on would be a cycle; nothing written", p.Parent, p.Before, p.Parent, p.Before)
	}

	res := &workflowPlugResult{Project: p.Project, Template: inst.Template, Version: inst.Version, Instance: inst.Key, Before: p.Before, PR: p.PR}
	desc := fmt.Sprintf("Workflow %s v%d (%s) for %s.\nSnapshot of the definition at plug time; editing workflows/%s.json does not change this block.\n\n%s",
		inst.Template, inst.Version, inst.Hash, p.PR, inst.Template, inst.Snapshot)

	if container == nil {
		id, err := bc.Create(ctx, dir, inst.Title, beads.CreateOptions{Parent: p.Parent, Labels: inst.Labels, Description: desc, Silent: true})
		if err != nil {
			return res, fmt.Errorf("creating the block failed (%v); rerun the same command to resume", err)
		}
		container = &beads.Bead{ID: id, Title: inst.Title, Status: "open"}
		res.Created = append(res.Created, id)
	}
	res.Container = container.ID

	ids := map[string]string{}
	for _, s := range inst.Steps {
		b, ok := steps[s.ID]
		if !ok {
			labels := s.Labels
			if len(s.After) == 0 {
				// A step nothing precedes is born held: the Manager dispatches it (atomic birth).
				now := time.Now()
				w := beads.Wait{Kind: beads.WaitUntil, Since: now, Until: now.Add(2 * time.Minute), Ref: beads.ManagerDispatchWait}
				labels = append(slices.Clone(labels), w.Label())
			}
			id, err := bc.Create(ctx, dir, s.Title, beads.CreateOptions{Parent: container.ID, Labels: labels, Silent: true})
			if err != nil {
				return res, fmt.Errorf("creating step %s failed (%v); %d created so far, rerun the same command to resume", s.ID, err, len(res.Created))
			}
			b = beads.Bead{ID: id, Title: s.Title, Status: "open"}
			steps[s.ID] = b
			res.Created = append(res.Created, id)
		}
		ids[s.ID] = b.ID
	}
	for _, s := range inst.Steps {
		for _, w := range s.After {
			if err := ensureWaits(ctx, bc, dir, ids[s.ID], ids[w]); err != nil {
				return res, fmt.Errorf("step %s: %v; rerun the same command to resume", s.ID, err)
			}
		}
	}
	if err := ensureWaits(ctx, bc, dir, p.Before, container.ID); err != nil {
		return res, fmt.Errorf("making %s wait on the block: %v; rerun the same command to resume", p.Before, err)
	}

	for _, s := range inst.Steps {
		b := steps[s.ID]
		if b.Status != "closed" {
			res.Remaining = append(res.Remaining, workflowStepOut{Step: s.ID, BeadID: b.ID, Title: s.Title, Status: b.Status})
		}
	}
	if len(res.Remaining) == 0 {
		res.Note = "every step is closed; nothing was changed or closed by this command"
	} else {
		res.Note = "close each remaining step when its check is met: cmdr internal-close-objective --project " + p.Project + " --bead <id>; this command never closes anything"
	}
	return res, nil
}

// ensureWaits adds the edge from waits on dep unless it is already there.
func ensureWaits(ctx context.Context, bc beads.Client, dir, from, dep string) error {
	deps, err := bc.Deps(ctx, dir, from)
	if err != nil {
		return err
	}
	for _, d := range deps {
		if d.DependsOnID == dep && d.Type == "blocks" {
			return nil
		}
	}
	return bc.DependOn(ctx, dir, from, dep)
}
