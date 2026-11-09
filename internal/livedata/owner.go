package livedata

import (
	"context"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// ownerIn is the bead whose Manager owns id among loaded rows: the nearest row
// up its parents (id first) that Owns, else the top of the chain it can walk
// (the root, as before nested Managers). A bead not among the rows is its own
// top. Rows of a project whose nested Managers are off never Own.
func ownerIn(byID map[string]ObjectiveRow, id string) string {
	top := id
	seen := map[string]bool{}
	for cur := id; cur != "" && !seen[cur]; {
		seen[cur] = true
		r, ok := byID[cur]
		if !ok {
			break
		}
		if r.Owns {
			return cur
		}
		top = cur
		cur = r.ParentBeadID
	}
	return top
}

// rowsByID indexes rows by bead id.
func rowsByID(rows []ObjectiveRow) map[string]ObjectiveRow {
	out := make(map[string]ObjectiveRow, len(rows))
	for _, r := range rows {
		if r.BeadID != "" {
			out[r.BeadID] = r
		}
	}
	return out
}

// OwnerOf is the workstream whose Manager owns a bead, from loaded data: its
// nearest nested workstream with a Manager of its own while its project's nested
// Managers are on, else its root. A bead that is no row (an Operator's task)
// walks its parents to the first loaded one.
func (d Data) OwnerOf(bead string) string {
	return ownerByParents(rowsByID(d.Objectives), d.Parents(), bead)
}

func ownerByParents(byID map[string]ObjectiveRow, parents beads.Parents, bead string) string {
	for _, id := range beads.Lineage(context.Background(), nil, "", parents, bead) {
		if _, ok := byID[id]; ok {
			return ownerIn(byID, id)
		}
	}
	return beads.Root(context.Background(), nil, "", parents, bead)
}

// ManagerOf is the mail role of OwnerOf's Manager.
func (d Data) ManagerOf(bead string) string {
	return store.WorkstreamManagerRole(d.OwnerOf(bead))
}

// workstreamOwners lists ws's nested owners (rows that Own), parents first.
func workstreamOwners(ws Workstream) []string {
	var out []string
	for _, n := range ws.Steps {
		if n.Row.Owns {
			out = append(out, n.Row.BeadID)
		}
	}
	return out
}
