package beads

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func nestedDir(t *testing.T, on bool) string {
	t.Helper()
	dir := t.TempDir()
	if err := SetNestedManagers(dir, on); err != nil {
		t.Fatal(err)
	}
	if NestedManagersOn(dir) != on {
		t.Fatalf("switch = %v, want %v", !on, on)
	}
	return dir
}

func TestOwner_NearestOpenWorkstream(t *testing.T) {
	ctx := context.Background()
	c := NestedTree()
	dir := nestedDir(t, true)
	for id, want := range map[string]string{
		NestedStep:  NestedA1, // nearest, not A or R
		NestedA1:    NestedA1, // itself first
		NestedA:     NestedA,
		NestedBStep: NestedB,
		NestedRoot:  NestedRoot,
	} {
		if got := Owner(ctx, c, dir, nil, nil, id); got != want {
			t.Errorf("Owner(%s) = %s, want %s", id, got, want)
		}
	}
}

func TestOwner_LegacyTrackIsNotAnOwner(t *testing.T) {
	c := NestedTree()
	dir := nestedDir(t, true)
	for _, id := range []string{NestedTrack, NestedTStep} {
		if got := Owner(context.Background(), c, dir, nil, nil, id); got != NestedRoot {
			t.Errorf("Owner(%s) = %s, want the root", id, got)
		}
	}
}

func TestOwner_ClosedAndParkedSkip(t *testing.T) {
	c := NestedTree()
	dir := nestedDir(t, true)
	for _, id := range []string{NestedClosed, NestedCStep, NestedParked, NestedPStep, NestedP1, NestedP1Step} {
		if got := Owner(context.Background(), c, dir, nil, nil, id); got != NestedRoot {
			t.Errorf("Owner(%s) = %s, want the root", id, got)
		}
	}
}

func TestOwner_SwitchOffIsRoot(t *testing.T) {
	c := NestedTree()
	dir := nestedDir(t, false)
	if got := Owner(context.Background(), c, dir, nil, nil, NestedStep); got != NestedRoot {
		t.Errorf("switch off: Owner = %s, want root", got)
	}
}

type listFails struct{ *MemClient }

func (listFails) List(context.Context, string, ListOptions) ([]Bead, error) {
	return nil, errors.New("busy")
}

func TestOwner_UnreadableLabelsFallBackToRoot(t *testing.T) {
	c := listFails{NestedTree()}
	dir := nestedDir(t, true)
	if got := Owner(context.Background(), c, dir, nil, nil, NestedStep); got != NestedRoot {
		t.Errorf("unreadable labels: Owner = %s, want root", got)
	}
}

func TestOwner_LoadedLabelsNeedNoRead(t *testing.T) {
	tree := NestedTree()
	dir := nestedDir(t, true)
	known := Parents{NestedStep: NestedA1, NestedA1: NestedA, NestedA: NestedRoot, NestedRoot: ""}
	labels := func(id string) (Bead, bool) {
		b := tree.find(id)
		if b == nil {
			return Bead{}, false
		}
		return *b, true
	}
	// No client: everything from loaded data.
	if got := Owner(context.Background(), nil, dir, known, labels, NestedStep); got != NestedA1 {
		t.Errorf("loaded: Owner = %s, want %s", got, NestedA1)
	}
}

func TestOwnerChain_NearestFirstEndingAtRoot(t *testing.T) {
	tree := NestedTree()
	got := map[string]Bead{}
	for _, b := range tree.Beads {
		got[b.ID] = *b
	}
	chain := []string{NestedStep, NestedA1, NestedA, NestedRoot}
	want := []string{NestedA1, NestedA, NestedRoot}
	if oc := OwnerChain(chain, got); len(oc) != 3 || oc[0] != want[0] || oc[1] != want[1] || oc[2] != want[2] {
		t.Fatalf("OwnerChain = %v, want %v", oc, want)
	}
}

// The switch is written whole (no temporary left beside it) and a switch that
// cannot be read reads as off.
func TestSetNestedManagers_AtomicAndUnreadableIsOff(t *testing.T) {
	dir := t.TempDir()
	if err := SetNestedManagers(dir, true); err != nil || !NestedManagersOn(dir) {
		t.Fatalf("on: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, ".commander"))
	if len(entries) != 1 || entries[0].Name() != NestedManagersFile {
		t.Fatalf("left beside the switch: %v", entries)
	}
	if os.Getuid() != 0 {
		if err := os.Chmod(filepath.Join(dir, ".commander"), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, ".commander"), 0o755) })
		if NestedManagersOn(dir) {
			t.Fatal("an unreadable switch read as on")
		}
	}
}
