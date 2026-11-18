package beads

import (
	"fmt"
	"sort"
	"strings"
)

// Plan templates are the recurring workstream shapes as ready plan-apply
// operations. Each one passes ShapeCheck by construction, so starting from a
// template is the easy way to build a tree the guard accepts.

type planStep struct {
	ref, title, labels string
	after              []string
}

// prSteps are the PR flow both PR templates share up to the review.
func prSteps(s string) []planStep {
	return []planStep{
		{ref: "build", title: "Build " + s + " and open its PR (review skill once)", labels: "kind:objective,lane:agents"},
		{ref: "green", title: s + " PR: get CI green and answer Devin (fix our failures; a red check from main is reported with its cause and owner)", labels: "kind:objective,lane:agents", after: []string{"build"}},
		{ref: "check", title: "Review: the " + s + " PR (green, Devin-clean, matches the scope)", labels: "kind:objective,lane:agents,review", after: []string{"green"}},
	}
}

// PlanTemplateTitles are the templates' display names, by slug.
var PlanTemplateTitles = map[string]string{
	"pr-babysit-fast":      "Ship a PR, fast (admin merge)",
	"pr-babysit-slow":      "Ship a PR, with team review",
	"pr-babysit":           "Ship a PR, fast (admin merge)",
	"propose-then-approve": "Propose, approve, then build",
	"investigation":        "Investigate and decide a fix",
	"app-release":          "Release an app to prod",
	"teach-agents":         "Teach agents a topic with a Learn Forge guide",
}

var planTemplates = map[string]func(subject string, ownerGo bool) []planStep{
	"pr-babysit-fast": func(s string, _ bool) []planStep {
		return append(prSteps(s),
			planStep{ref: "merge", title: "Greenlight: admin-merge the " + s + " PR (no team review)", labels: "kind:objective,gate,approver:user", after: []string{"check"}},
			planStep{ref: "live", title: s + " live: wait on the deploy (--waiting deploy:<merge sha>, not a clock) and confirm it works on a real event", labels: "kind:objective,lane:agents", after: []string{"merge"}},
		)
	},
	"pr-babysit-slow": func(s string, _ bool) []planStep {
		return append(prSteps(s),
			planStep{ref: "team", title: "The team approves the " + s + " PR on GitHub (wait pr:approved:#N once the PR is open)", labels: "kind:objective,lane:elsewhere:the team", after: []string{"check"}},
			planStep{ref: "merge", title: "Greenlight: merge the " + s + " PR", labels: "kind:objective,gate,approver:user", after: []string{"team", "check"}},
			planStep{ref: "live", title: s + " live: wait on the deploy (--waiting deploy:<merge sha>, not a clock) and confirm it works on a real event", labels: "kind:objective,lane:agents", after: []string{"merge"}},
		)
	},
	"app-release": func(s string, ownerGo bool) []planStep {
		steps := []planStep{
			{ref: "train", title: "Release " + s + ": assemble the release on the dev box", labels: "kind:objective,lane:agents"},
			{ref: "fix", title: "Release " + s + ": fix or drop every failing change (no weakened tests; dropped changes move to the next release with the reason)", labels: "kind:objective,lane:agents", after: []string{"train"}},
			{ref: "validate", title: "Release " + s + ": one full validate on the final tree, receipt written (dev box quiet)", labels: "kind:objective,lane:agents", after: []string{"fix"}},
		}
		deployAfter := "validate"
		if ownerGo {
			steps = append(steps,
				planStep{ref: "check", title: "Review: release " + s + " (validate green on the final tree, receipt written, what was dropped and why)", labels: "kind:objective,lane:agents,review", after: []string{"validate"}},
				planStep{ref: "go", title: "Greenlight shipping release " + s, labels: "kind:objective,gate,approver:user", after: []string{"check"}})
			deployAfter = "go"
		}
		return append(steps,
			planStep{ref: "deploy", title: "Release " + s + ": deploy to prod reusing the receipt (zero downtime, memory gate)", labels: "kind:objective,lane:agents,cmdr:release-deploy", after: []string{deployAfter}},
			planStep{ref: "watch", title: "Release " + s + ": 15-minute prod watch, then post DEPLOYED", labels: "kind:objective,lane:agents", after: []string{"deploy"}},
			planStep{ref: "mainline", title: "Release " + s + ": land the release on the app's mainline", labels: "kind:objective,lane:agents", after: []string{"watch"}},
		)
	},
	"propose-then-approve": func(s string, _ bool) []planStep {
		return []planStep{
			{ref: "propose", title: "Propose " + s + " (what, why, and how it will be checked)", labels: "kind:objective,lane:agents"},
			{ref: "check", title: "Review: the " + s + " proposal (complete, matches the scope)", labels: "kind:objective,lane:agents,review", after: []string{"propose"}},
			{ref: "approve", title: "Approve " + s, labels: "kind:objective,gate,approver:user", after: []string{"check"}},
			{ref: "build", title: "Build " + s + " and open its PR (review skill once)", labels: "kind:objective,lane:agents", after: []string{"approve"}},
			{ref: "green", title: s + " PR: get CI green and answer Devin", labels: "kind:objective,lane:agents", after: []string{"build"}},
			{ref: "check2", title: "Review: the " + s + " PR (green, Devin-clean, matches the approved proposal)", labels: "kind:objective,lane:agents,review", after: []string{"green"}},
			{ref: "merge", title: "Greenlight: merge the " + s + " PR", labels: "kind:objective,gate,approver:user", after: []string{"check2"}},
			{ref: "live", title: s + " live: wait on the deploy (--waiting deploy:<merge sha>, not a clock) and confirm it works", labels: "kind:objective,lane:agents", after: []string{"merge"}},
		}
	},
	"teach-agents": func(s string, _ bool) []planStep {
		const l = "kind:objective,lane:agents"
		return []planStep{
			{ref: "ux", title: "Teach agents " + s + ": align on the experience (what a user asks, what the agent should do and say)", labels: l},
			{ref: "bench", title: "Teach agents " + s + ": ForgeBench knowledge-only questions that can only be answered by using the Learn Forge guide", labels: l, after: []string{"ux"}},
			{ref: "guide", title: "Teach agents " + s + ": write the Learn Forge guide (a non-technical part for humans, an agent part) and a general context-control nudge with nothing hardcoded", labels: l, after: []string{"bench"}},
			{ref: "run", title: "Teach agents " + s + ": run the bench and iterate on the guide and nudge until the cases pass", labels: l, after: []string{"guide"}},
			{ref: "check", title: "Review: the " + s + " guide, nudge and bench results (cases pass, guide is used, nothing hardcoded)", labels: l + ",review", after: []string{"run"}},
			{ref: "live", title: "Teach agents " + s + ": make the guide and nudge live", labels: l, after: []string{"check"}},
			{ref: "soak", title: "Teach agents " + s + ": 48 h soak, watching real experiences", labels: l, after: []string{"live"}},
			{ref: "check2", title: "Review: the " + s + " soak (what real agents did, what to fix)", labels: l + ",review", after: []string{"soak"}},
			{ref: "eric", title: "Eric: accept the " + s + " teaching (the only human step)", labels: "kind:objective,gate,approver:user", after: []string{"check2"}},
		}
	},
	"investigation": func(s string, _ bool) []planStep {
		return []planStep{
			{ref: "investigate", title: "Investigate " + s + ": reproduce and find the cause, with evidence", labels: "kind:objective,lane:agents"},
			{ref: "check", title: "Review: the " + s + " findings (cause proven, evidence attached)", labels: "kind:objective,lane:agents,review", after: []string{"investigate"}},
			{ref: "decide", title: "Decide the " + s + " fix: proposed change, size and risk", labels: "kind:objective,gate,approver:user", after: []string{"check"}},
		}
	},
}

func init() {
	// pr-babysit is the old name of the fast PR template.
	planTemplates["pr-babysit"] = planTemplates["pr-babysit-fast"]
}

// PlanTemplateNames lists the templates in a stable order.
func PlanTemplateNames() []string {
	out := make([]string, 0, len(planTemplates))
	for n := range planTemplates {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// PlanTemplateOps returns plan-apply operations that create a template's
// steps under parent, chained so each waits on the one before it.
func PlanTemplateOps(name, parent, subject string, ownerGo bool) ([][]string, error) {
	build, ok := planTemplates[name]
	if !ok {
		return nil, fmt.Errorf("unknown plan template %q (have %s)", name, strings.Join(PlanTemplateNames(), ", "))
	}
	if strings.TrimSpace(parent) == "" || strings.TrimSpace(subject) == "" {
		return nil, fmt.Errorf("plan template needs a parent and a subject")
	}
	var ops [][]string
	for _, s := range build(strings.TrimSpace(subject), ownerGo) {
		title := s.title
		if r := []rune(title); len(r) > 0 {
			title = strings.ToUpper(string(r[0])) + string(r[1:])
		}
		op := []string{"create", title, "--parent", parent, "--labels", s.labels, "--ref", s.ref, "--silent"}
		if desc, acc := stepBrief(s, title); desc != "" {
			op = append(op, "--description", desc, "--acceptance", acc)
		}
		ops = append(ops, op)
	}
	for _, s := range build(strings.TrimSpace(subject), ownerGo) {
		for _, a := range s.after {
			ops = append(ops, []string{"dep", "add", "$" + s.ref, "$" + a})
		}
	}
	return ops, nil
}

// stepBrief is an agent step's brief, from its own title: a review step checks
// the result of the steps it waits on, independently of the agent that built it.
func stepBrief(s planStep, title string) (description, acceptance string) {
	if !strings.Contains(s.labels, "lane:agents") {
		return "", ""
	}
	if strings.Contains(s.labels, "review") {
		return "Review brief: " + title + ". You are an independent reviewer, not the agent that did the work. Read the result of the step(s) this waits on (their final reports, PR, run output or live evidence) and check each point named in the title against it.",
			"Final report states PASS with the evidence for each point, or BLOCKED naming each gap and where it is."
	}
	return title + ".", "Final report says what was done, with the evidence (PR, run, output) that it is done."
}
