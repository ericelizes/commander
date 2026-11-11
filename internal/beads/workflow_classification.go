package beads

import (
	"regexp"
	"slices"
	"strings"
)

func workflowLabel(b Bead, prefix string) string {
	for _, l := range b.Labels {
		if v, ok := strings.CutPrefix(l, prefix); ok {
			return v
		}
	}
	return ""
}

// mergeWord is merge or merged as a word of its own: "pre-merge" is not the merge.
var mergeWord = regexp.MustCompile(`(?i)(?:^|[^\w-])merged?(?:$|[^\w-])`)

// inPlay is a bead the merge rule can apply to: not finished, not dropped from
// the plan, not a generated workflow bead (no recursion).
func WorkflowInPlay(b Bead) bool {
	return b.Status != "closed" && b.Status != "tombstone" && workflowLabel(b, "workflow-step:") == "" && workflowLabel(b, "workflow-template:") == "" &&
		!slices.Contains(b.Labels, "superseded") && !slices.Contains(b.Labels, "retired")
}

// MergeGate is a gate that passes when the PR merges.
func WorkflowMergeGate(b Bead) bool {
	return WorkflowInPlay(b) && (slices.Contains(b.Labels, "gate-name:merged") || (slices.Contains(b.Labels, "gate") && mergeWord.MatchString(b.Title)))
}

// stepMergeWord is a merge verb in a step title: merge, merged, merges, merging,
// also after a hyphen ("Squash-merge PR 12"). "merger" and "mergeable" are none.
var stepMergeWord = regexp.MustCompile(`(?i)\bmerg(?:e|ed|es|ing)\b`)

// notTheMerge is work about merging that is not merging the PR: conflicts, syncing
// main into a branch, the merge queue, pre-/post-merge checks, merge commits, and
// "merge request" (GitLab's word for a PR, a noun).
var notTheMerge = regexp.MustCompile(`(?i)\bmerg(?:e|ed|es|ing)\s+conflicts?\b|` +
	`\bmerg(?:e|ed|es|ing)\s+(?:(?:origin|upstream)/)?(?:main|master)\s+(?:in)?to\b|` +
	`\bmerge\s+queues?\b|\b(?:pre|post)-merge\b|\bmerge\s+commits?\b|\bmerge\s+requests?\b`)

// isMergeStepTitle fails closed: a title with a merge word is the merge unless every
// merge word sits in an exclusion ("Fix merge conflicts" no, "Fix merge conflicts
// and merge PR 12" yes, "Squash and merge PR 12" yes).
func isMergeStepTitle(title string) bool {
	return stepMergeWord.MatchString(notTheMerge.ReplaceAllString(title, " "))
}

// MergeTarget is what the doors and graph check hold to the rule: a merge gate, or
// a step with a PR whose title says it is the merge. A pr: label alone only says
// which PR a step tracks (internal-set-objective --review writes it on ordinary work).
func WorkflowMergeTarget(b Bead) bool {
	return WorkflowMergeGate(b) || (WorkflowInPlay(b) && workflowLabel(b, "pr:") != "" && isMergeStepTitle(b.Title))
}

// Guardable is a bead that waiting on a review block turns into a merge target.
func WorkflowGuardable(b Bead) bool { return WorkflowInPlay(b) }
