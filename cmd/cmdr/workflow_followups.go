package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/workflowtemplate"
)

// Follow-up labels: the finding's key (idempotent filing) and the block it came
// from. Both sit under the reserved workflow- prefix, so no agent writes them.
const (
	followUpLabel   = "workflow-followup:"
	followUpOfLabel = "workflow-followup-of:"
)

// followUpKey names one finding of one review on one head of one step: filing
// again (a retried close, a head re-read) finds the bead instead of a second one.
func followUpKey(instance, step, review, head, finding string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{instance, step, review, head, finding}, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// fileReviewFollowUps files each minor finding a passing review left unfixed
// (workflowtemplate.MinorPolicy) as a plain step under the owning Manager's
// workstream, never under the block's stage (an open child would hold the
// merge). Each carries a "Manager to start" receipt, so the board reads it as
// the Manager's move, and a discovered-from link to the block.
func fileReviewFollowUps(ctx context.Context, bc beads.Client, dir string, block beads.Bead, instance, step, review, head string, findings []workflowtemplate.ReviewFinding) ([]string, error) {
	owner := beads.Owner(ctx, bc, dir, nil, nil, block.ID)
	if owner == "" {
		return nil, fmt.Errorf("cannot find the workstream that owns block %s", block.ID)
	}
	manager := store.WorkstreamManagerRole(owner)
	ids := make([]string, 0, len(findings))
	for _, f := range findings {
		if f.Severity != "minor" {
			return nil, fmt.Errorf("finding %s is %s: only minor findings become follow-ups", f.ID, f.Severity)
		}
		label := followUpLabel + followUpKey(instance, step, review, head, f.ID)
		found, err := bc.List(ctx, dir, beads.ListOptions{Status: "all", Labels: []string{label}})
		if err != nil {
			return nil, err
		}
		if len(found) > 1 {
			return nil, fmt.Errorf("%d follow-up beads carry %s; keep one", len(found), label)
		}
		if len(found) == 1 {
			ids = append(ids, found[0].ID)
			continue
		}
		title := "Review follow-up: " + oneLine(f.Summary, 80)
		desc := fmt.Sprintf("Minor finding %s left unfixed by review %s on head %s (block %s, step %s). Filed by the review gate under %s: it no longer holds the merge. Holder: %s.\n\n%s",
			f.ID, review, head, block.ID, step, workflowtemplate.MinorPolicy, manager, f.Summary)
		id, err := bc.Create(ctx, dir, title, beads.CreateOptions{
			Parent:      owner,
			Type:        "task",
			Labels:      []string{"kind:objective", label, followUpOfLabel + beads.EscapeLabel(block.ID)},
			Description: desc,
			Silent:      true,
		})
		if err != nil {
			return nil, fmt.Errorf("filing a follow-up for %s: %w", f.ID, err)
		}
		if err := bc.Link(ctx, dir, id, block.ID, "discovered-from"); err != nil {
			fmt.Fprintf(os.Stderr, "warning: follow-up %s was filed but not linked to block %s: %v\n", id, block.ID, err)
		}
		ids = append(ids, id)
	}
	// Steps already held (a wait, an Operator, a retry's earlier receipt) are left alone.
	if _, err := holdForManager(ctx, bc, dir, ids, "", time.Now()); err != nil {
		return nil, fmt.Errorf("follow-ups %s were filed but their Manager's hold was not written: %w", strings.Join(ids, ", "), err)
	}
	return ids, nil
}

// oneLine flattens s to one line of at most n runes.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
