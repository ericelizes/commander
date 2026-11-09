// Package approvedcontext restores only the retained context of a closed user
// approval. It never synthesizes scope from a question, answer, or caller text.
package approvedcontext

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/wsmgr"
)

type Result struct {
	Bead          string `json:"bead"`
	Generation    string `json:"generation"`
	AnswerID      string `json:"answer_id"`
	ContextSHA256 string `json:"context_sha256"`
	DryRun        bool   `json:"dry_run"`
	Changed       bool   `json:"changed"`
}

func retained(p store.Pitch, r store.NeedsResolution) (string, error) {
	if r.Generation != strconv.Itoa(p.Version) || r.ProjectID != p.ProjectID || r.BeadID != p.BeadID || r.AnswerID == "" || (r.State != store.ResolutionClosed && r.State != store.ResolutionMailed) || p.AnsweredAt == 0 {
		return "", fmt.Errorf("missing completed exact-generation approval")
	}
	approved := r.Action == "approve" && p.Answer == "Approved"
	if r.Action == "decide" && p.Answer == r.Choice {
		d, err := store.ParseDecision(p.Decision)
		if err != nil {
			return "", err
		}
		for _, o := range d.Options {
			if o.Label == r.Choice && o.Approves {
				approved = true
			}
		}
	}
	if !approved {
		return "", fmt.Errorf("stored answer is not an approval of this pitch")
	}
	d, err := store.ParseDecision(p.Decision)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(d.Context) == "" {
		return "", fmt.Errorf("approved pitch has no context")
	}
	return d.Context, nil
}

// Restore uses the ordinary audited Beads write client. The state handle may be
// read-only and never receives a write, including on retries and dry runs.
func Restore(ctx context.Context, st *store.Store, bc beads.Client, p store.Project, id, role string, generation int, answerID string, dryRun bool) (Result, error) {
	result := Result{Bead: id, DryRun: dryRun}
	// The nearest owner or any owner above it (the root's alone while the
	// project's nested Managers are off).
	owners, err := wsmgr.OwnerChain(ctx, bc, p.Path, id)
	if err != nil {
		return result, err
	}
	if role != "" && role != "user" && role != "commander" && !slices.ContainsFunc(owners, func(o string) bool { return role == wsmgr.Role(o) }) {
		return result, fmt.Errorf("only the owning Manager, Commander or user may restore approved context")
	}
	pitch, r, err := st.ApprovedContextEvidence(ctx, p.ID, id)
	if err != nil {
		return result, err
	}
	if generation != pitch.Version || answerID != r.AnswerID {
		return result, fmt.Errorf("generation/answer ID does not match latest stored approval")
	}
	scope, err := retained(pitch, r)
	if err != nil {
		return result, err
	}
	result.Generation = r.Generation
	result.AnswerID = r.AnswerID
	result.ContextSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(scope)))
	// The CLI supplies an uncached Beads client: Show reads the backend here.
	b, err := bc.Show(ctx, p.Path, id)
	if err != nil {
		return result, err
	}
	if b == nil {
		return result, fmt.Errorf("gate unavailable")
	}
	if b.Status != "closed" || (!slices.Contains(b.Labels, "gate") && !slices.Contains(b.Labels, "kind:gate")) || !slices.Contains(b.Labels, "approver:user") {
		return result, fmt.Errorf("requires a closed user gate")
	}
	reader, ok := bc.(beads.ExactDescriptionReader)
	if !ok {
		return result, fmt.Errorf("backend cannot read exact Description")
	}
	description, err := reader.ExactDescription(ctx, p.Path, id)
	if err != nil {
		return result, err
	}
	if description == scope {
		return result, nil
	}
	if description != "" {
		return result, fmt.Errorf("gate already has different context; refusing overwrite")
	}
	if dryRun {
		return result, nil
	}
	if err := bc.UpdateFields(ctx, p.Path, id, beads.Fields{Description: &scope}); err != nil {
		return result, err
	}
	result.Changed = true
	return result, nil
}
