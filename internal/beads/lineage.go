package beads

import (
	"context"
	"log"
)

// Parents maps bead ids to their parent by parent-child edge ("" for a
// top-level bead), for beads a caller has already read.
type Parents map[string]string

// Lineage is id followed by its ancestors by parent-child edge, nearest first.
// Its last entry is the top-level bead, the workstream that owns id. A bead
// promoted to a workstream keeps its dotted id, and a moved bead keeps the id it
// was created with, so the dotted id never decides ownership.
//
// Parents come from known first, then from c: one batched Details call covers
// id and every dotted prefix of it (the likely ancestors), and one more is made
// for each ancestor outside that set. Only a bead whose parent can be read from
// neither falls back to its dotted parent; with a nil c (or no dir) that
// fallback is silent (the caller chose to work from loaded data), else logged.
func Lineage(ctx context.Context, c Client, dir string, known Parents, id string) []string {
	if id == "" {
		return nil
	}
	if dir == "" {
		c = nil // br would read whatever project the cwd is in
	}
	read := map[string]string{}
	asked := map[string]bool{}
	var chain []string
	seen := map[string]bool{}
	for cur := id; cur != "" && !seen[cur]; {
		seen[cur] = true
		chain = append(chain, cur)
		parent, ok := known[cur]
		if !ok {
			parent, ok = read[cur]
		}
		if !ok && c != nil && !asked[cur] {
			var batch []string
			for p := cur; p != ""; p = ParentOf(p) {
				if _, have := known[p]; !have && !asked[p] {
					batch = append(batch, p)
					asked[p] = true
				}
			}
			got, err := FreshDetails(ctx, c, dir, batch)
			if err != nil {
				log.Printf("beads: parents of %v in %s: %v", batch, dir, err)
			}
			for bid, d := range got {
				read[bid] = d.Parent
			}
			parent, ok = read[cur]
		}
		if !ok {
			if c != nil {
				log.Printf("beads: parent of %s in %s unreadable; using its dotted id", cur, dir)
			}
			parent = ParentOf(cur)
		}
		cur = parent
	}
	return chain
}

// Root is the workstream (top-level) bead that owns id; see Lineage. "" for "".
func Root(ctx context.Context, c Client, dir string, known Parents, id string) string {
	chain := Lineage(ctx, c, dir, known, id)
	if len(chain) == 0 {
		return ""
	}
	return chain[len(chain)-1]
}
