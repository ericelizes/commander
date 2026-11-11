package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/example-user/commander/internal/gates"
	"github.com/example-user/commander/internal/store"
)

// gateTemplates is the standard lifecycle: for build work the stages ARE the
// gates, one gate each. Non-build stages use investigation, outward or spend.
var gateTemplates = []string{"spec", "pr-ready", "reviewed", "pr-approved", "merged", "live", "pilot", "investigation", "outward", "spend"}

// templateArgs are the per-stage inputs `internal-gate apply` takes.
type templateArgs struct {
	Repo      string   // owner/repo, for the PR templates
	PRs       []string // PR numbers, merged in this order for stacks
	Reviewers []string // pr-approved: the named reviewers
	LiveCheck string   // live: an optional runnable check (else an Operator wires it)
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(strings.TrimPrefix(p, "#")); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func ghRepoFlag(repo string) string {
	if repo == "" {
		return ""
	}
	return " --repo " + repo
}

// templateGate builds the one standard gate for a template.
func templateGate(template string, a templateArgs) (store.Gate, error) {
	needPRs := func() error {
		if len(a.PRs) == 0 {
			return fmt.Errorf("template %s needs --pr <n>[,<n>...] (and --repo owner/repo)", template)
		}
		return nil
	}
	switch template {
	case "spec":
		return store.Gate{Name: "spec", Verifier: store.VerifierUser, Check: "Spec approved by the user (link the spec in the evidence)"}, nil
	case "pr-ready":
		if err := needPRs(); err != nil {
			return store.Gate{}, err
		}
		var parts []string
		for _, pr := range a.PRs {
			parts = append(parts, fmt.Sprintf("gh pr checks %s%s >/dev/null", pr, ghRepoFlag(a.Repo)))
			if a.Repo != "" {
				owner, name, _ := strings.Cut(a.Repo, "/")
				parts = append(parts, fmt.Sprintf(`[ "$(gh api graphql -f query='{repository(owner:"%s",name:"%s"){pullRequest(number:%s){reviewThreads(first:100){nodes{isResolved}}}}}' --jq '[.data.repository.pullRequest.reviewThreads.nodes[]|select(.isResolved|not)]|length')" = 0 ]`, owner, name, pr))
			}
		}
		return store.Gate{Name: "pr-ready", Verifier: store.VerifierAuto, Check: strings.Join(parts, " && ")}, nil
	case "reviewed":
		return store.Gate{Name: "reviewed", Verifier: store.VerifierOperator,
			Check: "A separate review Operator ran /ce-code-review on the diff against the acceptance criteria and reported CLEAN (evidence: the bead id of that clean review report)"}, nil
	case "pr-approved":
		if len(a.Reviewers) == 0 {
			return store.Gate{}, fmt.Errorf("template pr-approved needs --reviewers <login>[,<login>...]")
		}
		return store.Gate{Name: "pr-approved", Verifier: store.VerifierReviewerPrefix + a.Reviewers[0],
			Check: "Approved by " + strings.Join(a.Reviewers, ", ") + " on every PR (gh pr view <n> --json reviews)"}, nil
	case "merged":
		if err := needPRs(); err != nil {
			return store.Gate{}, err
		}
		// in the order given: stacks merge bottom-up
		return store.Gate{Name: "merged", Verifier: store.VerifierAuto, Check: gates.MergedCheck(a.PRs, a.Repo)}, nil
	case "live":
		if a.LiveCheck != "" {
			return store.Gate{Name: "live", Verifier: store.VerifierAuto, Check: a.LiveCheck}, nil
		}
		return store.Gate{Name: "live", Verifier: store.VerifierOperator, Check: "Deployed, and one real working check passed (record the evidence: URL, query or log line)"}, nil
	case "pilot":
		return store.Gate{Name: "pilot", Verifier: store.VerifierUser, Check: "Pilot metric met and the user's go/no-go (record the metric and the decision)"}, nil
	case "investigation":
		return store.Gate{Name: "investigation", Verifier: store.VerifierOperator, Check: "Root cause found, with evidence (queries, logs, a repro)"}, nil
	case "outward":
		return store.Gate{Name: "outward", Verifier: store.VerifierUser, Check: "the user approves the exact outward message text before it is sent"}, nil
	case "spend":
		return store.Gate{Name: "spend", Verifier: store.VerifierUser, Check: "the user approves the spend / personal or regulated data / production-data action before it runs"}, nil
	}
	known := append([]string(nil), gateTemplates...)
	sort.Strings(known)
	return store.Gate{}, fmt.Errorf("unknown template %q (want one of %s)", template, strings.Join(known, ", "))
}

// gateApply makes template the stage's single gate: the stage = the template,
// so any other gate on the bead is dropped.
func gateApply(ctx context.Context, st *store.Store, projectName, beadID, template string, a templateArgs, noPR, evidence string) (any, error) {
	g, err := templateGate(template, a)
	if err != nil {
		return nil, err
	}
	p, err := gateProject(ctx, st, projectName, beadID)
	if err != nil {
		return nil, err
	}
	bc := newBeadsClientFn()
	waiver, err := newNoPRWaiver(ctx, bc, p.Path, beadID, noPR, evidence)
	if err != nil {
		return nil, err
	}
	existing, err := stageGates(ctx, bc, p, beadID)
	if err != nil {
		return nil, err
	}
	var dropped []string
	for _, e := range existing {
		if e.Gate.Name != g.Name {
			if err := checkWorkflowCloseWaiving(ctx, bc, p.Path, p.Name, e.ID, waiver); err != nil {
				return nil, err
			}
			if err := requireGateClosesHeld(ctx, bc, p.Path, e.ID, "internal-gate apply (it retires "+e.Gate.Name+")"); err != nil {
				return nil, err
			}
			retireCtx, ctxErr := noPRCompletionContext(ctx, bc, p.Path, e.ID, waiver)
			if ctxErr != nil {
				return nil, ctxErr
			}
			if err := gates.Remove(retireCtx, bc, p.Path, p.ID, beadID, e.Gate.Name); err != nil {
				return nil, err
			}
			dropped = append(dropped, e.Gate.Name)
		}
	}
	out, err := gateAdd(ctx, st, projectName, beadID, g, "standard lifecycle template")
	if err != nil {
		return nil, err
	}
	if m, ok := out.(map[string]any); ok && len(dropped) > 0 {
		m["dropped"] = dropped
	}
	return out, nil
}
