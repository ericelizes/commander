package gates

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

const PublicationRepairPrefix = "gate-publication-repair@v1 "

// RepairPublication explicitly removes only redundant strict-ancestor waits,
// then publishes the original gate. A wait still guarding other live work under
// its waiter is not redundant and refuses. An intent is durable before any edge
// write. The whole project graph is judged once, before the first write; between
// writes only the gate, leaf, chain and waiters are read again. Repeating the
// exact request can finish an interrupted operation, including a write whose
// response was lost. No rollback can weaken the protected leaf wait.
func RepairPublication(ctx context.Context, bc beads.Client, dir, id, parent string, waiters []string, reason, actor string, dry bool) error {
	if len(waiters) == 0 || strings.TrimSpace(reason) == "" {
		return fmt.Errorf("publication repair requires --repair-ancestor-waits and --reason")
	}
	waiters = slices.Clone(waiters)
	slices.Sort(waiters)
	for i, w := range waiters {
		if w == "" || (i > 0 && w == waiters[i-1]) {
			return fmt.Errorf("publication repair: empty or duplicate waiter")
		}
	}
	if actor == "" {
		actor = "user"
	}
	// The CLI also rejects Operators (including legacy inherited-role callers).
	if actor != "user" && actor != "commander" && !strings.HasPrefix(actor, "manager-") {
		return fmt.Errorf("publication repair: unauthorized caller %s", actor)
	}
	validate := func(full bool) (map[string]beads.Detail, string, error) {
		if err := PublishPlan(ctx, bc, dir, id, parent, true); err != nil {
			return nil, "", err
		}
		card, err := beads.FreshShow(ctx, bc, dir, id)
		if err != nil {
			return nil, "", err
		}
		if card == nil || card.Status != "open" || beads.Superseded(*card) || slices.Contains(card.Labels, RetiredLabel) || !slices.Contains(card.Labels, "verifier:user") || !slices.Contains(card.Labels, "approver:user") {
			return nil, "", fmt.Errorf("publication repair requires an open user approval gate")
		}
		leaf := unescapeLabel(labelValue(card.Labels, stageLabelPfx))
		count := 0
		for _, l := range card.Labels {
			if strings.HasPrefix(l, stageLabelPfx) {
				count++
			}
		}
		if count != 1 {
			return nil, "", fmt.Errorf("publication repair: ambiguous gate-of")
		}
		ids := append([]string{id, parent, leaf}, waiters...)
		seen := map[string]bool{}
		statuses := map[string]string{}
		if full {
			// Read the complete project graph with a fail-closed cap. Details bypasses
			// display caches; referenced endpoints missing from List are read as well.
			rows, err := bc.List(ctx, dir, beads.ListOptions{Status: "all", Limit: 100000})
			if err != nil {
				return nil, "", err
			}
			if len(rows) >= 100000 {
				return nil, "", fmt.Errorf("publication repair: graph exceeds read cap")
			}
			for _, b := range rows {
				ids = append(ids, b.ID)
				statuses[b.ID] = b.Status
			}
		}
		graph := map[string]beads.Detail{}
		for len(ids) > 0 {
			batch := []string{}
			for _, x := range ids {
				if !seen[x] {
					seen[x] = true
					batch = append(batch, x)
				}
			}
			ids = nil
			if len(seen) > 100000 {
				return nil, "", fmt.Errorf("publication repair: graph exceeds read cap")
			}
			if len(batch) == 0 {
				break
			}
			ds, err := beads.FreshDetails(ctx, bc, dir, batch)
			if err != nil {
				return nil, "", err
			}
			for _, x := range batch {
				d, ok := ds[x]
				if !ok {
					return nil, "", fmt.Errorf("publication repair: missing graph node %s", x)
				}
				parents := 0
				for _, e := range d.Deps {
					if e.IssueID != x || e.DependsOnID == "" {
						return nil, "", fmt.Errorf("publication repair: malformed graph edge on %s", x)
					}
					if e.Type == "parent-child" {
						parents++
						if e.DependsOnID != d.Parent {
							return nil, "", fmt.Errorf("publication repair: conflicting parent on %s", x)
						}
					}
				}
				if parents > 1 {
					return nil, "", fmt.Errorf("publication repair: multiple parents on %s", x)
				}
				graph[x] = d
				if d.Parent != "" {
					ids = append(ids, d.Parent)
				}
				if !full {
					continue
				}
				for _, e := range d.Deps {
					if e.Type == "blocks" || e.Type == "parent-child" {
						ids = append(ids, e.DependsOnID)
					}
				}
			}
		}
		if graph[leaf].Parent != parent || (graph[id].Parent != "" && graph[id].Parent != parent) {
			return nil, "", fmt.Errorf("publication repair: parent changed during preflight")
		}
		root := ""
		ancestors := map[string]bool{}
		var upward []string // parent first, the root last
		for cur := parent; cur != ""; cur = graph[cur].Parent {
			if ancestors[cur] {
				return nil, "", fmt.Errorf("publication repair: ancestry cycle")
			}
			ancestors[cur] = true
			upward = append(upward, cur)
			root = cur
			if !full { // PublishPlan just judged this chain
				continue
			}
			ancestor, err := beads.FreshShow(ctx, bc, dir, cur)
			if err != nil {
				return nil, "", err
			}
			if ancestor == nil || !publicationObjective(*ancestor) || slices.Contains(ancestor.Labels, "hold") || (graph[cur].Parent == "" && slices.Contains(ancestor.Labels, beads.TrackLabel)) {
				return nil, "", fmt.Errorf("publication repair: invalid or held ancestor %s", cur)
			}
		}
		// A nested owner repairs only inside its own subtree: no waiter above
		// its workstream bead.
		below := map[string]bool{}
		if actor != "user" && actor != "commander" && actor != store.WorkstreamManagerRole(root) {
			own := nestedOwnerOf(ctx, bc, dir, upward, actor)
			if own == "" {
				return nil, "", fmt.Errorf("publication repair: only owning Manager %s may repair", store.WorkstreamManagerRole(root))
			}
			for _, a := range upward {
				below[a] = true
				if a == own {
					break
				}
			}
			for _, w := range waiters {
				if ancestors[w] && !below[w] {
					return nil, "", fmt.Errorf("publication repair: %s is above %s's workstream %s; its Manager or one above repairs it", w, actor, own)
				}
			}
		}
		if !repairWait(graph[leaf], id) {
			return nil, "", fmt.Errorf("publication repair: protected leaf %s must wait on gate", leaf)
		}
		payload, _ := json.Marshal(struct {
			Gate, Parent, Leaf, Root, Reason, Actor string
			Waiters                                 []string
		}{id, parent, leaf, root, reason, actor, waiters})
		intent := PublicationRepairPrefix + string(payload)
		noted := false
		for _, c := range graph[id].Comments {
			if c.Text == intent {
				noted = true
			}
		}
		for _, w := range waiters {
			if w == leaf || w == root || !ancestors[w] {
				return nil, "", fmt.Errorf("publication repair: %s is not a strict non-root guarded ancestor", w)
			}
			edges := 0
			for _, e := range graph[w].Deps {
				if e.DependsOnID == id {
					if e.Type != "blocks" {
						return nil, "", fmt.Errorf("publication repair: refusing non-block edge from %s", w)
					}
					edges++
				}
			}
			if edges > 1 || (edges == 0 && !noted) {
				return nil, "", fmt.Errorf("publication repair: missing/ambiguous unaudited wait from %s", w)
			}
		}
		if !full {
			return graph, intent, nil
		}
		// Judge the whole resulting graph, including the proposed parent and every
		// preserved gate dependency. Parent-child and blocks both participate in bd
		// cycle detection; unrelated relationship types do not.
		color := map[string]int{}
		var visit func(string) error
		visit = func(x string) error {
			if color[x] == 1 {
				return fmt.Errorf("publication repair: resulting dependency cycle at %s", x)
			}
			if color[x] == 2 {
				return nil
			}
			color[x] = 1
			d := graph[x]
			p := d.Parent
			if x == id {
				p = parent
			}
			if p != "" {
				if err := visit(p); err != nil {
					return err
				}
			}
			for _, e := range d.Deps {
				if e.Type != "blocks" && e.Type != "parent-child" {
					continue
				}
				if e.Type == "parent-child" {
					continue
				}
				if e.DependsOnID == id && slices.Contains(waiters, x) {
					continue
				}
				if err := visit(e.DependsOnID); err != nil {
					return err
				}
			}
			color[x] = 2
			return nil
		}
		for x := range graph {
			if err := visit(x); err != nil {
				return nil, "", err
			}
		}
		if x, w := releasedDescendant(graph, statuses, id, leaf, ancestors, waiters); x != "" {
			return nil, "", fmt.Errorf("publication repair: removing %s's wait would release live %s, which has no wait of its own on %s; add one or leave that wait in place", w, x, id)
		}
		return graph, intent, nil
	}
	graph, intent, err := validate(true)
	if err != nil || dry {
		return err
	}
	noted := false
	for _, c := range graph[id].Comments {
		if c.Text == intent {
			noted = true
		}
	}
	if !noted {
		if err = bc.AddComment(ctx, dir, id, intent); err != nil {
			return fmt.Errorf("publication repair: intent audit failed; no edge writes: %w", err)
		}
	}
	revalidate := func() (map[string]beads.Detail, error) {
		current, nextIntent, e := validate(false)
		if e == nil && nextIntent != intent {
			e = fmt.Errorf("publication repair: ancestry changed since intent; stop and inspect")
		}
		if e == nil {
			found := false
			for _, c := range current[id].Comments {
				found = found || c.Text == intent
			}
			if !found {
				e = fmt.Errorf("publication repair: durable intent missing on fresh read")
			}
		}
		return current, e
	}
	for _, w := range waiters {
		graph, err = revalidate()
		if err != nil {
			return err
		}
		if !repairWait(graph[w], id) {
			continue
		}
		if err = bc.RemoveDep(ctx, dir, w, id); err != nil {
			return fmt.Errorf("publication repair interrupted at %s; repeat exact request to reconcile possibly applied writes: %w", w, err)
		}
	}
	graph, err = revalidate()
	if err != nil {
		return err
	}
	if graph[id].Parent != parent {
		if err = bc.SetParent(ctx, dir, id, parent); err != nil {
			return fmt.Errorf("publication repair parent write interrupted; repeat exact request: %w", err)
		}
	}
	done := intent + " completed"
	ds, err := beads.FreshDetails(ctx, bc, dir, []string{id})
	if err != nil {
		return err
	}
	for _, c := range ds[id].Comments {
		if c.Text == done {
			return nil
		}
	}
	if err = bc.AddComment(ctx, dir, id, done); err != nil {
		return fmt.Errorf("publication repair applied; completion audit failed; repeat exact request: %w", err)
	}
	return nil
}

// releasedDescendant names a live bead that only a listed waiter's wait on the
// gate keeps from starting, and that waiter. Only a closed bead is not live:
// parked and superseded-but-open work can come back into play. The gate, the
// leaf and the leaf's chain are exempt, and so is what sits under the gate, which
// no waiter's wait held. Keeping a wait means a live, unlisted bead has the edge.
// The graph must already be known free of parent cycles.
func releasedDescendant(graph map[string]beads.Detail, statuses map[string]string, id, leaf string, chain map[string]bool, waiters []string) (string, string) {
	live := func(x string) bool { return statuses[x] != "closed" && statuses[x] != "tombstone" }
	nodes := make([]string, 0, len(graph))
	for x := range graph {
		nodes = append(nodes, x)
	}
	slices.Sort(nodes)
	for _, x := range nodes {
		if x == id || x == leaf || chain[x] || !live(x) || repairWait(graph[x], id) {
			continue
		}
		under, held := "", false
		for cur := graph[x].Parent; cur != "" && !held; cur = graph[cur].Parent {
			switch {
			case cur == id:
				held = true
			case slices.Contains(waiters, cur):
				if under == "" {
					under = cur
				}
			case statuses[cur] != "" && live(cur) && repairWait(graph[cur], id):
				held = true
			}
		}
		if under != "" && !held {
			return x, under
		}
	}
	return "", ""
}

func repairWait(d beads.Detail, id string) bool {
	for _, e := range d.Deps {
		if e.Type == "blocks" && e.DependsOnID == id {
			return true
		}
	}
	return false
}

// nestedOwnerOf: with the project's nested Managers on, actor is the Manager
// (either role spelling) of an open nested workstream among upward (the nearest
// owner, or one above it, may repair beside the root's Manager); the answer is
// that workstream's bead, "" for no. A failed read answers no.
func nestedOwnerOf(ctx context.Context, bc beads.Client, dir string, upward []string, actor string) string {
	if !strings.HasPrefix(actor, "manager-") || !beads.NestedManagersOn(dir) {
		return ""
	}
	for _, a := range upward {
		if beads.OwnerLabelName("manager-"+a) != beads.OwnerLabelName(actor) {
			continue
		}
		b, err := beads.FreshShow(ctx, bc, dir, a)
		if err == nil && b != nil && beads.OwnsWork(*b) {
			return a
		}
		return ""
	}
	return ""
}
