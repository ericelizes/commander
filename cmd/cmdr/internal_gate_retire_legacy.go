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
	"github.com/example-user/commander/internal/wsmgr"
)

const legacyRetirementPrefix = "gate-retire-legacy@v1 "

// Label removals cannot be atomic with validation. Durable intent and fresh
// checks between writes permit recovery; they are not backend compare-and-swap.
func gateRetireLegacy(ctx context.Context, st *store.Store, project, id, replacement, reason string, dry bool) error {
	role := os.Getenv("COMMANDER_ROLE")
	if callerIsOperator(role) {
		return fmt.Errorf("retire-legacy: Operators must ask the owning Manager")
	}
	if replacement == "" || replacement == id || strings.TrimSpace(reason) == "" {
		return fmt.Errorf("retire-legacy requires distinct --replacement and --reason")
	}
	p, err := st.GetProjectByName(ctx, project)
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("project %q not added", project)
	}
	bc := freshReader(newBeadsClientFn())
	actor := role
	if actor == "" {
		actor = "user"
	}
	type request struct{ Stage, Replacement, Reason, Actor, Root, Digest string }
	validate := func() (*beads.Bead, string, bool, error) {
		fail := func(s string) (*beads.Bead, string, bool, error) {
			return nil, "", false, fmt.Errorf("retire-legacy: %s", s)
		}
		b, e := beads.FreshShow(ctx, bc, p.Path, id)
		if e != nil {
			return nil, "", false, e
		}
		if b == nil || b.ID != id {
			return fail("missing stage")
		}
		g, e := beads.FreshShow(ctx, bc, p.Path, replacement)
		if e != nil {
			return nil, "", false, e
		}
		if g == nil || g.ID != replacement {
			return fail("missing replacement")
		}
		safe := func(x beads.Bead) bool {
			if x.Status != "open" || beads.Superseded(x) {
				return false
			}
			for _, l := range x.Labels {
				if l == "hold" || l == "retired" || l == "approved" || l == beads.DecisionAnsweredLabel || strings.HasPrefix(l, "proposed:") || strings.HasPrefix(l, "gate-status:") {
					return false
				}
			}
			return true
		}
		if !safe(*b) || !safe(*g) {
			return fail("stage and replacement must be open, unheld, unproposed and unapproved")
		}
		if !slices.Contains(b.Labels, "kind:objective") || slices.Contains(b.Labels, "kind:gate") || slices.Contains(b.Labels, "kind:task") || slices.Contains(b.Labels, "kind:track") || slices.Contains(b.Labels, "track") || slices.Contains(b.Labels, beads.WorkstreamLabel) {
			return fail("target must be an ordinary objective stage")
		}
		for _, l := range b.Labels {
			if strings.HasPrefix(l, "gate-of:") || strings.HasPrefix(l, "verifier:") || (strings.HasPrefix(l, "approver:") && l != "approver:user") || l == "needs-user" {
				return fail("target has separate gate/card authority")
			}
		}
		if !slices.Contains(g.Labels, "kind:gate") || !slices.Contains(g.Labels, "verifier:user") || !slices.Contains(g.Labels, "approver:user") || !slices.Contains(g.Labels, "needs-user") {
			return fail("replacement must be a genuine user approval gate with a live card")
		}
		links := 0
		for _, l := range g.Labels {
			if strings.HasPrefix(l, "gate-of:") {
				links++
				if l != "gate-of:"+beads.EscapeLabel(id) {
					return fail("wrong replacement linkage")
				}
			}
			if (strings.HasPrefix(l, "verifier:") && l != "verifier:user") || (strings.HasPrefix(l, "approver:") && l != "approver:user") {
				return fail("ambiguous replacement verifier")
			}
		}
		if links != 1 {
			return fail("ambiguous replacement linkage")
		}
		ds, e := beads.FreshDetails(ctx, bc, p.Path, []string{id, replacement})
		if e != nil {
			return nil, "", false, e
		}
		sd, ok := ds[id]
		if !ok {
			return fail("missing stage details")
		}
		gd, ok := ds[replacement]
		if !ok {
			return fail("missing replacement details")
		}
		if sd.Parent == "" || gd.Parent != sd.Parent {
			return fail("stage and replacement must have the same real parent")
		}
		if !retirementDetail(id, sd) || !retirementDetail(replacement, gd) {
			return fail("ambiguous parent or malformed dependency evidence")
		}
		waits := 0
		for _, edge := range sd.Deps {
			if edge.DependsOnID == replacement && edge.Type == "blocks" {
				waits++
			}
		}
		if waits != 1 {
			return fail("stage must retain exactly one blocking wait on replacement")
		}
		// The audit record keeps the root it always named; any owner over the
		// stage may act.
		root, e := wsmgr.StrictRoot(ctx, bc, p.Path, id)
		if e != nil {
			return nil, "", false, e
		}
		owners, e := ownerRoles(ctx, bc, p.Path, id)
		if e != nil {
			return nil, "", false, e
		}
		if role != "" && role != "commander" && !roleAmong(role, owners) {
			return fail("only owning Manager " + owners[0] + ", Commander or user may retire legacy markers")
		}
		seen := map[string]bool{}
		for cur := sd.Parent; cur != ""; {
			if seen[cur] {
				return fail("ancestry cycle")
			}
			seen[cur] = true
			a, e := beads.FreshShow(ctx, bc, p.Path, cur)
			if e != nil {
				return nil, "", false, e
			}
			if a == nil || !safe(*a) || !slices.Contains(a.Labels, "kind:objective") || isGateBead(*a) {
				return fail("invalid ancestor " + cur)
			}
			ad, e := beads.FreshDetails(ctx, bc, p.Path, []string{cur})
			if e != nil {
				return nil, "", false, e
			}
			d, ok := ad[cur]
			if !ok || !retirementDetail(cur, d) {
				return fail("missing or ambiguous ancestor")
			}
			cur = d.Parent
		}
		rows, e := bc.List(ctx, p.Path, beads.ListOptions{Labels: []string{"gate-of:" + beads.EscapeLabel(id)}, Status: "all", Limit: 100000})
		if e != nil {
			return nil, "", false, e
		}
		if len(rows) >= 100000 {
			return fail("project exceeds read cap")
		}
		found := false
		for _, r := range rows {
			x, e := beads.FreshShow(ctx, bc, p.Path, r.ID)
			if e != nil {
				return nil, "", false, e
			}
			if x == nil {
				return fail("incomplete project read")
			}
			if slices.Contains(x.Labels, "gate-of:"+beads.EscapeLabel(id)) {
				if x.ID != replacement {
					return fail("multiple attached gates")
				}
				found = true
			}
		}
		if !found {
			return fail("replacement missing from project")
		}
		own, e := st.GetNeedsYou(ctx, p.ID, id)
		if e != nil {
			return nil, "", false, e
		}
		if own != nil {
			return fail("stage has its own card")
		}
		card, e := st.GetNeedsYou(ctx, p.ID, replacement)
		if e != nil {
			return nil, "", false, e
		}
		if card == nil || card.Ask == "" || card.Decision == "" {
			return fail("missing retained replacement card")
		}
		if _, e := store.ParseDecision(card.Decision); e != nil {
			return fail("invalid retained decision")
		}
		pitches, e := st.ListPitches(ctx, p.ID, replacement)
		if e != nil {
			return nil, "", false, e
		}
		exact := []store.Pitch{}
		for _, v := range pitches {
			if v.BeadID == replacement {
				exact = append(exact, v)
			}
		}
		if len(exact) == 0 {
			return fail("missing replacement generation")
		}
		latest := exact[len(exact)-1]
		if latest.Answer != "" || latest.AnsweredAt != 0 || latest.Decision != card.Decision || latest.Title != card.Ask {
			return fail("replacement generation answered or mismatched")
		}
		resolutions, e := st.ListNeedsResolutions(ctx, p.ID, replacement)
		if e != nil {
			return nil, "", false, e
		}
		for _, r := range resolutions {
			if r.Generation == fmt.Sprint(latest.Version) {
				return fail("replacement generation already has an answer")
			}
		}
		normalized := *b
		normalized.UpdatedAt = ""
		normalized.CommentCount = 0
		normalized.Labels = slices.DeleteFunc(slices.Clone(b.Labels), func(l string) bool { return l == "gate" || l == "approver:user" })
		sd.Comments = nil
		payload, _ := json.Marshal([]any{normalized, sd, g, gd, card, exact, resolutions})
		digest := fmt.Sprintf("%x", sha256.Sum256(payload))
		raw, _ := json.Marshal(request{id, replacement, reason, actor, root, digest})
		note := legacyRetirementPrefix + string(raw)
		noted := false
		for _, c := range ds[id].Comments {
			if c.Text == note {
				noted = true
			}
		}
		if (!slices.Contains(b.Labels, "gate") || !slices.Contains(b.Labels, "approver:user")) && !noted {
			return fail("missing legacy markers without matching durable intent")
		}
		return b, note, noted, nil
	}
	_, intent, noted, err := validate()
	if err != nil || dry {
		return err
	}
	if !noted {
		if err = bc.AddComment(ctx, p.Path, id, intent); err != nil {
			return fmt.Errorf("retire-legacy: intent audit failed; repeat exact request: %w", err)
		}
	}
	for _, label := range []string{"gate", "approver:user"} {
		b, current, durable, e := validate()
		if e != nil {
			return e
		}
		if current != intent || !durable {
			return fmt.Errorf("retire-legacy: evidence changed or intent missing; stop and inspect")
		}
		if slices.Contains(b.Labels, label) {
			if err = bc.SetLabel(ctx, p.Path, id, label, false); err != nil {
				return fmt.Errorf("retire-legacy interrupted; repeat exact request: %w", err)
			}
		}
	}
	_, current, durable, err := validate()
	if err != nil {
		return err
	}
	if current != intent || !durable {
		return fmt.Errorf("retire-legacy: post-write evidence changed; inspect before retry")
	}
	ds, err := beads.FreshDetails(ctx, bc, p.Path, []string{id})
	if err != nil {
		return err
	}
	done := intent + " completed"
	for _, c := range ds[id].Comments {
		if c.Text == done {
			return nil
		}
	}
	if err = bc.AddComment(ctx, p.Path, id, done); err != nil {
		return fmt.Errorf("retire-legacy applied; repeat exact request for completion audit: %w", err)
	}
	return nil
}

// Details must agree with their actual parent edges; dotted IDs are not proof.
func retirementDetail(id string, d beads.Detail) bool {
	parents := 0
	for _, e := range d.Deps {
		if e.IssueID != id || e.DependsOnID == "" {
			return false
		}
		if e.Type == "parent-child" {
			parents++
			if e.DependsOnID != d.Parent {
				return false
			}
		}
	}
	return (d.Parent == "" && parents == 0) || (d.Parent != "" && parents == 1)
}
