package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

const withdrawalPrefix = "gate-withdrawal@v1 "

type withdrawalRequest struct {
	Bead, State, Snapshot, Reason, Instruction, Actor string
	Generation                                        int
}
type withdrawalIntent struct {
	Request withdrawalRequest
	Card    *store.NeedsYou
}

// gateWithdraw shares the answer lock with resolution. Backend writes are still
// separate: fresh validation and a retained intent support interrupted retries,
// not cross-process/cross-host compare-and-swap against arbitrary graph editors.
func gateWithdraw(ctx context.Context, st *store.Store, project string, r withdrawalRequest, dry bool) (any, error) {
	fail := func(s string) (any, error) { return nil, fmt.Errorf("withdraw: %s", s) }
	role := os.Getenv("COMMANDER_ROLE")
	if callerIsOperator(role) {
		return fail("Operators must ask the owning Manager")
	}
	if r.State != "open" || r.Generation < 1 || strings.TrimSpace(r.Reason) == "" || strings.TrimSpace(r.Instruction) == "" {
		return fail("requires --expected-state open, --generation, --reason and --instruction (durable cancellation citation)")
	}
	if !dry && len(r.Snapshot) != 64 {
		return fail("requires --expected-snapshot from a dry-run")
	}
	r.Actor = role
	if r.Actor == "" {
		r.Actor = "user"
	}
	release, err := lockNeedsResolution()
	if err != nil {
		return nil, err
	}
	defer release()
	p, err := st.GetProjectByName(ctx, project)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return fail("unknown project")
	}
	bc := freshReader(newBeadsClientFn())
	var intent string
	validate := func() (*beads.Bead, string, bool, error) {
		bad := func(s string) (*beads.Bead, string, bool, error) {
			return nil, "", false, fmt.Errorf("withdraw: %s", s)
		}
		b, e := beads.FreshShow(ctx, bc, p.Path, r.Bead)
		if e != nil {
			return nil, "", false, e
		}
		if b == nil || b.ID != r.Bead {
			return bad("missing exact gate")
		}
		if !slices.Contains(b.Labels, "kind:gate") && !(slices.Contains(b.Labels, "gate") && slices.Contains(b.Labels, "approver:user")) {
			return bad("target is not an explicit gate")
		}
		owners, e := gateOwnerRoles(ctx, bc, p.Path, *b)
		if e != nil {
			return nil, "", false, e
		}
		if role != "" && role != "commander" && !roleAmong(role, owners) {
			return bad("only the owning Manager, Commander or user may withdraw")
		}
		ds, e := beads.FreshDetails(ctx, bc, p.Path, []string{r.Bead})
		if e != nil {
			return nil, "", false, e
		}
		d, ok := ds[r.Bead]
		if !ok || !retirementDetail(r.Bead, d) {
			return bad("missing or ambiguous gate details")
		}
		var saved *withdrawalIntent
		for _, c := range d.Comments {
			if strings.HasPrefix(c.Text, withdrawalPrefix) && !strings.HasSuffix(c.Text, " completed") {
				var v withdrawalIntent
				if json.Unmarshal([]byte(strings.TrimPrefix(c.Text, withdrawalPrefix)), &v) != nil {
					return bad("malformed withdrawal intent")
				}
				// A different request cannot take over an interrupted cancellation.
				compare := r
				compare.Snapshot = v.Request.Snapshot
				if compare != v.Request || (r.Snapshot != "" && r.Snapshot != v.Request.Snapshot) {
					return bad("different withdrawal intent exists; repeat the exact original request")
				}
				if saved != nil {
					return bad("ambiguous withdrawal intents")
				}
				saved = &v
			}
			if strings.HasPrefix(c.Text, "gate passed by ") {
				return bad("gate already has approval evidence")
			}
		}
		withdrawn := slices.Contains(b.Labels, beads.WithdrawnLabel)
		if (b.Status != r.State && !(saved != nil && withdrawn && b.Status == beads.StatusDeferred)) || (withdrawn && saved == nil) {
			return bad("expected state changed or withdrawal has no matching intent")
		}
		if b.AssignedAgent != "" {
			return bad("gate has execution assignee " + b.AssignedAgent)
		}
		for _, l := range b.Labels {
			if l == "approved" || l == beads.DecisionAnsweredLabel || l == "retired" || l == "superseded" || l == "hold" || strings.HasPrefix(l, "gate-status:") || strings.HasPrefix(l, "proposed:") || strings.HasPrefix(l, "waiting:") || strings.HasPrefix(l, "owner:operator") || strings.HasPrefix(l, "owner:op-") {
				return bad("gate has approval, hold or execution marker " + l)
			}
		}
		runs, e := st.ListAgentRunsByProject(ctx, p.ID, "")
		if e != nil {
			return nil, "", false, e
		}
		for _, run := range runs {
			if run.TaskBeadID == r.Bead && (run.Status == "running" || run.Status == "exited") {
				return bad(fmt.Sprintf("gate has execution holder run %d", run.ID))
			}
		}
		// All incoming relationships are read fresh. Parked/superseded still count
		// as LIVE here; only a truly closed bead is exempt. Never unlink anything.
		rows, e := bc.List(ctx, p.Path, beads.ListOptions{Status: "all", Limit: 100000})
		if e != nil {
			return nil, "", false, e
		}
		if len(rows) >= 100000 {
			return bad("project exceeds read cap")
		}
		ids := make([]string, 0, len(rows))
		found := false
		for _, row := range rows {
			ids = append(ids, row.ID)
			found = found || row.ID == r.Bead
		}
		if !found {
			return bad("gate absent from project read")
		}
		graph, e := beads.FreshDetails(ctx, bc, p.Path, ids)
		if e != nil {
			return nil, "", false, e
		}
		for _, row := range rows {
			rd, ok := graph[row.ID]
			if !ok || !retirementDetail(row.ID, rd) {
				return bad("incomplete graph at " + row.ID)
			}
			incoming := rd.Parent == r.Bead
			for _, edge := range rd.Deps {
				if edge.DependsOnID == r.Bead && edge.Type == "blocks" {
					incoming = true
				}
			}
			if incoming {
				live, e := beads.FreshShow(ctx, bc, p.Path, row.ID)
				if e != nil {
					return nil, "", false, e
				}
				if live == nil || live.ID != row.ID {
					return bad("unreadable incoming bead " + row.ID)
				}
				if live.Status != "closed" {
					return bad("LIVE incoming waiter/child " + row.ID + "; dispose through existing supported controls first")
				}
			}
		}
		card, e := st.GetNeedsYou(ctx, p.ID, r.Bead)
		if e != nil {
			return nil, "", false, e
		}
		if card == nil {
			if saved == nil || !withdrawn || b.Status != beads.StatusDeferred {
				return bad("missing retained active card")
			}
			card = saved.Card
		}
		if card == nil || card.Decision == "" || card.Ask == "" {
			return bad("missing retained decision")
		}
		if _, e = store.ParseDecision(card.Decision); e != nil {
			return bad("invalid retained decision")
		}
		pitches, e := st.ListPitches(ctx, p.ID, r.Bead)
		if e != nil {
			return nil, "", false, e
		}
		exact := []store.Pitch{}
		for _, v := range pitches {
			if v.BeadID == r.Bead {
				if v.Answer != "" || v.AnsweredAt != 0 {
					return bad("gate has answered pitch history")
				}
				exact = append(exact, v)
			}
		}
		if len(exact) == 0 {
			return bad("missing generation")
		}
		latest := exact[len(exact)-1]
		if latest.Version != r.Generation || latest.Decision != card.Decision || latest.Title != card.Ask {
			return bad("expected generation/card changed")
		}
		resolutions, e := st.ListNeedsResolutions(ctx, p.ID, r.Bead)
		if e != nil {
			return nil, "", false, e
		}
		if len(resolutions) > 0 {
			return bad("gate has answer journal history")
		}
		normalized := *b
		normalized.Status = r.State
		normalized.UpdatedAt = ""
		normalized.CommentCount = 0
		normalized.Labels = slices.DeleteFunc(slices.Clone(b.Labels), func(l string) bool { return l == beads.WithdrawnLabel || l == beads.NeedsUserLabel })
		slices.Sort(normalized.Labels)
		d.Comments = nil
		payload, e := json.Marshal([]any{normalized, d, card, exact, resolutions})
		if e != nil {
			return nil, "", false, e
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(payload))
		if r.Snapshot != "" && r.Snapshot != digest {
			return bad("expected snapshot changed; nothing further written")
		}
		req := r
		req.Snapshot = digest
		raw, _ := json.Marshal(withdrawalIntent{req, card})
		note := withdrawalPrefix + string(raw)
		if saved != nil {
			old, _ := json.Marshal(saved)
			if note != withdrawalPrefix+string(old) {
				return bad("retained intent no longer matches snapshot")
			}
		}
		if saved == nil && (!slices.Contains(b.Labels, beads.NeedsUserLabel) || withdrawn) {
			return bad("gate has no active needs-user marker")
		}
		return b, note, saved != nil, nil
	}
	_, note, noted, err := validate()
	if err != nil {
		return nil, err
	}
	intent = note
	var parsed withdrawalIntent
	_ = json.Unmarshal([]byte(strings.TrimPrefix(intent, withdrawalPrefix)), &parsed)
	result := map[string]any{"bead_id": r.Bead, "dry_run": dry, "expected_snapshot": parsed.Request.Snapshot, "expected_state": r.State, "generation": r.Generation, "withdrawn": !dry}
	if dry {
		return result, nil
	}
	if !noted {
		if err = bc.AddComment(ctx, p.Path, r.Bead, intent); err != nil {
			return nil, fmt.Errorf("withdraw: intent audit failed; repeat exact request: %w", err)
		}
	}
	check := func() (*beads.Bead, error) {
		b, n, ok, e := validate()
		if e != nil {
			return nil, e
		}
		if !ok || n != intent {
			return nil, fmt.Errorf("withdraw: intent changed; stop and inspect")
		}
		return b, nil
	}
	b, err := check()
	if err != nil {
		return nil, err
	}
	if !slices.Contains(b.Labels, beads.WithdrawnLabel) {
		if err = bc.SetLabel(ctx, p.Path, r.Bead, beads.WithdrawnLabel, true); err != nil {
			return nil, err
		}
	}
	b, err = check()
	if err != nil {
		return nil, err
	}
	if b.Status != beads.StatusDeferred {
		if err = bc.SetStatus(ctx, p.Path, r.Bead, beads.StatusDeferred); err != nil {
			return nil, err
		}
	}
	b, err = check()
	if err != nil {
		return nil, err
	}
	if slices.Contains(b.Labels, beads.NeedsUserLabel) {
		if err = bc.SetLabel(ctx, p.Path, r.Bead, beads.NeedsUserLabel, false); err != nil {
			return nil, err
		}
	}
	if _, err = check(); err != nil {
		return nil, err
	}
	if err = st.DeleteNeedsYou(ctx, p.ID, r.Bead); err != nil {
		return nil, err
	}
	if _, err = check(); err != nil {
		return nil, err
	}
	ds, err := beads.FreshDetails(ctx, bc, p.Path, []string{r.Bead})
	if err != nil {
		return nil, err
	}
	done := intent + " completed"
	for _, c := range ds[r.Bead].Comments {
		if c.Text == done {
			return result, nil
		}
	}
	if err = bc.AddComment(ctx, p.Path, r.Bead, done); err != nil {
		return nil, err
	}
	return result, nil
}
