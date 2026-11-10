package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/gates"
	"github.com/example-user/commander/internal/workflowtemplate"
)

// noPRWaiverPrefix starts the comment a no-PR waiver leaves on the merge gate.
const noPRWaiverPrefix = "merge rule waived for a ship with no PR by "

// noPRWaiver lets a merge gate of a ship that has no PR (a deploy on a VM) pass,
// be removed or be replaced: `internal-gate pass|rm|apply --no-pr <reason>
// --evidence <the user's approval>`. It waives only the rule's "has no PR yet"
// finding (workflowtemplate.Finding.NoPR); a pr: label or a review block still
// needs its review. Who and why are recorded on the gate before it changes.
type noPRWaiver struct {
	By, Reason, Evidence string
}

func (w *noPRWaiver) comment() string {
	return fmt.Sprintf("%s%s: %s. Approval: %s", noPRWaiverPrefix, w.By, w.Reason, w.Evidence)
}

// onlyNoPR: every finding is target id's "has no PR yet".
func onlyNoPR(findings []workflowtemplate.Finding, id string) bool {
	for _, f := range findings {
		if !f.NoPR || f.Target != id {
			return false
		}
	}
	return len(findings) > 0
}

// checkPRRE: words in a gate's check that name a PR: `gh pr <verb> N` (the
// standard merged check, gates.MergedCheck), a /pull/N URL, "PR #N", "PR N",
// "pull request #N". A bare "#N" is not one.
var checkPRRE = regexp.MustCompile(`(?i)\bgh\s+pr\s+[a-z-]+\s+(?:\S*/pull/)?#?\d+\b|/pull/\d+\b|\b(?:PR|pull request)\s*#?\d+\b`)

// namesPR: the gate's own check names a PR. MergeRule reads only pr: labels and
// review blocks, so a gate made by `apply --template merged --pr N` reads as
// having no PR; the waiver must not take that gate for a ship with none.
func namesPR(b beads.Bead) bool {
	for _, text := range []string{b.Description, b.AcceptanceCriteria} {
		if gates.IsMergedCheck(strings.TrimSpace(text)) || checkPRRE.MatchString(text) {
			return true
		}
	}
	return false
}

// newNoPRWaiver judges the caller before anything is read or written: Operators
// never, a Manager only when it owns the stage, Commander and the user always.
// An empty reason is no waiver.
func newNoPRWaiver(ctx context.Context, bc beads.Client, dir, stage, reason, evidence string) (*noPRWaiver, error) {
	reason, evidence = flattenLine(reason), flattenLine(evidence)
	if reason == "" {
		return nil, nil
	}
	role := os.Getenv("COMMANDER_ROLE")
	if callerIsOperator("") {
		return nil, fmt.Errorf("refused: Operators cannot waive the merge rule (--no-pr); ask the owning Manager")
	}
	if len([]rune(reason)) < 15 {
		return nil, fmt.Errorf("--no-pr needs a reason of at least 15 characters: why this ship has no PR")
	}
	if evidence == "" {
		return nil, fmt.Errorf("--no-pr needs --evidence naming the user's approval of the ship (answer id or decision)")
	}
	if role != "" && role != "commander" {
		owners, err := ownerRoles(ctx, freshReader(bc), dir, stage)
		if err != nil {
			return nil, err
		}
		if !roleAmong(role, owners) {
			who := "the owning Manager"
			if len(owners) > 0 {
				who = owners[0]
			}
			return nil, fmt.Errorf("refused: only %s, Commander or the user may waive the merge rule on %s", who, stage)
		}
	}
	by := role
	if by == "" {
		by = "the user"
		if u := os.Getenv("USER"); u != "" {
			by = "the user (" + u + ")"
		}
	}
	return &noPRWaiver{By: by, Reason: reason, Evidence: evidence}, nil
}

func flattenLine(s string) string { return strings.Join(strings.Fields(s), " ") }
