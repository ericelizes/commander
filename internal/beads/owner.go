package beads

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// NestedManagersFile is the per-project switch for nested Managers: present under
// <project>/.commander, every routing site resolves a bead to its nearest open
// workstream (Owner); absent, to its root as before. A file and not a state.db
// column, so every site that only knows the project dir (spawn, livedata, the
// door) reads the same answer with no store handle. Written only by
// `cmdr internal-nest-managers --apply|--undo`.
const NestedManagersFile = "nested-managers"

// NestedManagersOn reports the project's switch. An empty dir is off. A switch
// that cannot be read (permissions, I/O) reads as off, the one-Manager tree
// whose root Manager always exists, and is logged once per dir and error.
func NestedManagersOn(dir string) bool {
	if dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, ".commander", NestedManagersFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		if _, seen := switchErrs.LoadOrStore(dir+"\x00"+err.Error(), true); !seen {
			log.Printf("beads: nested-Managers switch of %s unreadable (%v); reading it as off", dir, err)
		}
	}
	return err == nil
}

var switchErrs sync.Map

type nestedKey struct{}

// AssumeNestedManagers makes the owner resolvers under ctx read the project's
// switch as on. Only the cutover uses it: it registers every nested Manager
// (and writes its mail cursor) before the switch is flipped, so nothing is
// ever routed to a Manager that does not exist yet.
func AssumeNestedManagers(ctx context.Context) context.Context {
	return context.WithValue(ctx, nestedKey{}, true)
}

// NestedManagersOnCtx is NestedManagersOn, or true under AssumeNestedManagers.
func NestedManagersOnCtx(ctx context.Context, dir string) bool {
	if on, _ := ctx.Value(nestedKey{}).(bool); on && dir != "" {
		return true
	}
	return NestedManagersOn(dir)
}

// SetNestedManagers turns the project's switch on or off.
func SetNestedManagers(dir string, on bool) error {
	path := filepath.Join(dir, ".commander", NestedManagersFile)
	if !on {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Written whole or not at all: the switch is the file's presence, so a
	// partial write must never leave it on.
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+NestedManagersFile+"-*")
	if err != nil {
		return err
	}
	_, werr := tmp.WriteString("on\n")
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), 0o644)
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
	}
	return werr
}

// LabelsOf answers a bead's labels and status from data a caller already
// loaded; ok=false means unknown (Owner reads it).
type LabelsOf func(id string) (b Bead, ok bool)

// OwnsWork: an open (not closed, parked or superseded) kind:workstream bead. Only
// the new label counts: an unmigrated kind:track stays with its workstream.
func OwnsWork(b Bead) bool {
	if !slices.Contains(b.Labels, WorkstreamLabel) || slices.Contains(b.Labels, "kind:task") {
		return false
	}
	switch b.Status {
	case "closed", "tombstone", StatusDeferred:
		return false
	}
	return !Superseded(b)
}

// Owner is the workstream whose Manager owns id: the nearest bead of its
// Lineage (id first) that OwnsWork, else the top of the chain (Root). With the
// project's switch off it is Root. Labels come from known first, then one
// List over the chain; an unreadable chain falls back to Root (display only:
// actions use wsmgr.StrictOwner, which fails instead).
func Owner(ctx context.Context, c Client, dir string, known Parents, labels LabelsOf, id string) string {
	chain := Lineage(ctx, c, dir, known, id)
	if len(chain) == 0 {
		return ""
	}
	root := chain[len(chain)-1]
	if !NestedManagersOnCtx(ctx, dir) {
		return root
	}
	got := map[string]Bead{}
	var missing []string
	for _, b := range chain[:len(chain)-1] {
		if labels != nil {
			if v, ok := labels(b); ok {
				got[b] = v
				continue
			}
		}
		missing = append(missing, b)
	}
	if len(missing) > 0 && c != nil {
		list, err := c.List(ctx, dir, ListOptions{IDs: missing, Status: "all"})
		if err != nil {
			log.Printf("beads: owner of %s in %s: labels unreadable (%v); using its root", id, dir, err)
			return root
		}
		for _, b := range list {
			got[b.ID] = b
		}
	}
	return NearestOwner(chain, got)
}

// ChainOwners is the one rule for who owns work over chain (nearest first,
// ending at the top): every bead below the top that OwnsWork, read top down and
// stopping at the first kind:workstream bead that is out of play (closed,
// parked, superseded) or any closed bead, since a nested workstream under one is
// out of play with it (the board folds a closed bead's subtree away). Nearest
// first, the top left out. A bead missing from beads, or an open one with no
// kind:workstream label, neither owns nor stops the walk. Owner, StrictOwner,
// OwnerChain and the registration lists (wsmgr Owners, NestedOwners) all read
// this, so a bead routed to always has a Manager that can be started.
func ChainOwners(chain []string, beads map[string]Bead) []string {
	if len(chain) < 2 {
		return nil
	}
	var down []string
	for i := len(chain) - 2; i >= 0; i-- {
		b, ok := beads[chain[i]]
		if ok && (b.Status == "closed" || b.Status == "tombstone") && !slices.Contains(b.Labels, WorkstreamLabel) {
			break
		}
		if !ok || !slices.Contains(b.Labels, WorkstreamLabel) || slices.Contains(b.Labels, "kind:task") {
			// kind:task never registers (wsmgr refuses it), so it owns nothing.
			continue
		}
		if !OwnsWork(b) {
			break
		}
		down = append(down, chain[i])
	}
	slices.Reverse(down)
	return down
}

// NearestOwner is the nearest of ChainOwners, else the chain's top. Shared by
// Owner and wsmgr.StrictOwner so both read one rule.
func NearestOwner(chain []string, beads map[string]Bead) string {
	if len(chain) == 0 {
		return ""
	}
	if owners := ChainOwners(chain, beads); len(owners) > 0 {
		return owners[0]
	}
	return chain[len(chain)-1]
}

// OwnerChain is every owner over chain, nearest first, ending at the top: the
// Manager that owns the bead and each one that coordinates it.
func OwnerChain(chain []string, beads map[string]Bead) []string {
	if len(chain) == 0 {
		return nil
	}
	return append(ChainOwners(chain, beads), chain[len(chain)-1])
}
