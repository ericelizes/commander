package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// Real bd: Show reads the parent from bd's own field, so a nested workstream's
// edge is refused on add and can still be dropped; a gate or track refuses both.
// Every case makes its own beads, so an edge one leaves behind cannot decide the next.
func TestInternalDepEdgeRules_BDIntegration(t *testing.T) {
	if os.Getenv("COMMANDER_BD_INTEGRATION") == "" {
		t.Skip("set COMMANDER_BD_INTEGRATION=1 to run against a real bd server")
	}
	realBD, err := exec.LookPath("bd")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMMANDER_BEADS_BIN", "bd")
	t.Setenv("COMMANDER_WRITE_DOOR", "off")
	ctx := context.Background()
	c := beads.New()
	setupObjectiveProject(t, c)
	st, err := store.Open(filepath.Join(os.Getenv("COMMANDER_HOME"), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	project, err := st.GetProjectByName(ctx, "manifest")
	if err != nil {
		t.Fatal(err)
	}
	dir := project.Path
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := c.Init(ctx, dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command(realBD, "-C", dir, "dolt", "stop").Run() })

	create := func(title, parent string, labels ...string) string {
		t.Helper()
		id, err := c.Create(ctx, dir, title, beads.CreateOptions{Parent: parent, Labels: append([]string{"kind:objective"}, labels...), Silent: true})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	root := create("Root", "", beads.WorkstreamLabel)
	nested := func() string { return create("Nested", root, beads.WorkstreamLabel) }
	step := func() string { return create("Step", root) }
	top := func() string { return create("Top", "", beads.WorkstreamLabel) }
	track := func() string { return create("Track", root, beads.TrackLabel) }
	gate := func() string { return create("Gate", root, beads.GateLabel) }
	hasEdge := func(a, b string) bool {
		t.Helper()
		deps, err := c.Deps(ctx, dir, a)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range deps {
			if d.DependsOnID == b && d.Type == "blocks" {
				return true
			}
		}
		return false
	}
	dep := func(verb, a, b string) (int, string) {
		t.Helper()
		return runVerb(t, func(o, e *os.File) int {
			return runInternalDep([]string{"--project", "manifest", verb, a, b}, o, e)
		})
	}

	t.Run("show reads the parent", func(t *testing.T) {
		n := nested()
		b, err := c.Show(ctx, dir, n)
		if err != nil || b.ParentID != root {
			t.Fatalf("nested ParentID = %q (%v), want %s", b.ParentID, err, root)
		}
		if b, err := c.Show(ctx, dir, root); err != nil || b.ParentID != "" {
			t.Fatalf("top-level ParentID = %q (%v), want none", b.ParentID, err)
		}
	})

	// add refused, the edge absent; rm of an edge made underneath allowed.
	for name, ends := range map[string]func() (string, string){
		"nested to nested": func() (string, string) { return nested(), nested() },
		"step to nested":   func() (string, string) { return step(), nested() },
		"nested to step":   func() (string, string) { return nested(), step() },
	} {
		t.Run(name, func(t *testing.T) {
			a, b := ends()
			code, out := dep("add", a, b)
			if code == 0 || !strings.Contains(out, "kind:workstream bead") || !strings.Contains(out, "internal-gate link") {
				t.Fatalf("add: exit %d: %s", code, out)
			}
			if hasEdge(a, b) {
				t.Fatal("a refused add left an edge")
			}
			if err := c.DependOn(ctx, dir, a, b); err != nil {
				t.Fatal(err)
			}
			if code, out := dep("rm", a, b); code != 0 {
				t.Fatalf("rm: exit %d: %s", code, out)
			}
			if hasEdge(a, b) {
				t.Fatal("rm left the edge")
			}
		})
	}

	// A gate or track is refused at either end, add and rm, and the edge stays.
	for name, mk := range map[string]func() string{"kind:track": track, beads.GateLabel: gate} {
		for side, ends := range map[string]func(x string) (string, string){
			"as the waiter":  func(x string) (string, string) { return x, step() },
			"as waited upon": func(x string) (string, string) { return step(), x },
		} {
			t.Run(name+" "+side, func(t *testing.T) {
				a, b := ends(mk())
				code, out := dep("add", a, b)
				if code == 0 || !strings.Contains(out, "bead: its edges") {
					t.Fatalf("add: exit %d: %s", code, out)
				}
				if hasEdge(a, b) {
					t.Fatal("a refused add left an edge")
				}
				if err := c.DependOn(ctx, dir, a, b); err != nil {
					t.Fatal(err)
				}
				code, out = dep("rm", a, b)
				if code == 0 || !strings.Contains(out, "bead: its edges") {
					t.Fatalf("rm: exit %d: %s", code, out)
				}
				if !hasEdge(a, b) {
					t.Fatal("a refused rm removed the edge")
				}
			})
		}
	}

	allowed := func(t *testing.T, a, b string) {
		t.Helper()
		if code, out := dep("add", a, b); code != 0 {
			t.Fatalf("add: exit %d: %s", code, out)
		}
		if !hasEdge(a, b) {
			t.Fatal("add wrote no edge")
		}
		if code, out := dep("rm", a, b); code != 0 {
			t.Fatalf("rm: exit %d: %s", code, out)
		}
		if hasEdge(a, b) {
			t.Fatal("rm left the edge")
		}
	}
	t.Run("step to step under sibling nested workstreams", func(t *testing.T) {
		allowed(t, create("Step", nested()), create("Step", nested()))
	})
	t.Run("top-level to top-level", func(t *testing.T) { allowed(t, top(), top()) })
	t.Run("step to top-level", func(t *testing.T) { allowed(t, step(), top()) })

	t.Run("gate link nested after nested", func(t *testing.T) {
		a, b := nested(), nested()
		if _, err := gateLink(ctx, st, "manifest", a, []string{b}, nil); err != nil {
			t.Fatal(err)
		}
		if !hasEdge(a, b) {
			t.Fatal("gateLink wrote no edge")
		}
	})

	t.Run("a description line sets no parent", func(t *testing.T) {
		id, err := c.Create(ctx, dir, "Top", beads.CreateOptions{Description: "Parent: nothing-real", Labels: []string{"kind:objective", beads.WorkstreamLabel}, Silent: true})
		if err != nil {
			t.Fatal(err)
		}
		b, err := c.Show(ctx, dir, id)
		if err != nil || b.ParentID != "" {
			t.Fatalf("ParentID = %q (%v), want none", b.ParentID, err)
		}
		allowed(t, id, top())
	})

	// bd's text form puts the description above its LABELS line, so these lines
	// once replaced the real labels, type and assignee.
	const forged = "Labels: plain\nType: epic\nAssigned: mallory\nOwner: mallory"
	t.Run("a description line does not hide a gate", func(t *testing.T) {
		id, err := c.Create(ctx, dir, "Merge gate", beads.CreateOptions{Parent: root, Description: forged, Labels: []string{"kind:objective", beads.GateLabel}, Silent: true})
		if err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(realBD, "-C", dir, "update", id, "--assignee", "alice").CombinedOutput(); err != nil {
			t.Fatalf("bd update: %v %s", err, out)
		}
		s := step()
		for _, verb := range []string{"add", "rm"} {
			if code, out := dep(verb, id, s); code == 0 || !strings.Contains(out, id+" is a gate bead") {
				t.Fatalf("%s: exit %d: %s", verb, code, out)
			}
		}
		if hasEdge(id, s) {
			t.Fatal("a refused add left an edge")
		}
		b, err := c.Show(ctx, dir, id)
		if err != nil || !slices.Contains(b.Labels, beads.GateLabel) || slices.Contains(b.Labels, "plain") || b.Type != "task" || b.AssignedAgent != "alice" {
			t.Fatalf("Show = %+v (%v), want the real labels, type task, assignee alice", b, err)
		}
	})
	t.Run("body lines do not make a plain bead a gate", func(t *testing.T) {
		text := "Labels: gate\nType: epic\nAssigned: mallory"
		id, err := c.Create(ctx, dir, "Plain", beads.CreateOptions{Description: text, Silent: true})
		if err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(realBD, "-C", dir, "comments", "add", id, text).CombinedOutput(); err != nil {
			t.Fatalf("bd comments add: %v %s", err, out)
		}
		if out, err := exec.Command(realBD, "-C", dir, "update", id, "--acceptance", text).CombinedOutput(); err != nil {
			t.Fatalf("bd update: %v %s", err, out)
		}
		b, err := c.Show(ctx, dir, id)
		if err != nil || len(b.Labels) != 0 || b.Type != "task" || b.AssignedAgent != "" {
			t.Fatalf("Show = %+v (%v), want no labels, type task, no assignee", b, err)
		}
		allowed(t, id, top())
	})
}
