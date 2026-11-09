package wsmgr

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tui"
)

func TestWakeUnknownOrNestedRoleNeverRescansFleet(t *testing.T) {
	for _, registered := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "registered nested"}[registered], func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			role := "manager-example-app-t11.1"
			if registered {
				if err := f.st.UpsertWorkstreamManager(ctx, store.WorkstreamManager{ProjectID: f.proj.ID, WorkstreamBeadID: "example-app-t11.1", AmRole: role, SessionFile: filepath.Join(t.TempDir(), "session")}); err != nil {
					t.Fatal(err)
				}
			}
			fresh := 0
			f.m.Numbered = func(context.Context) ([]tui.NumberedWorkstream, error) { return nil, nil }
			f.m.Fresh = func(context.Context) ([]tui.NumberedWorkstream, error) {
				fresh++
				return nil, errors.New("full fleet rescan must not run for mail")
			}
			f.m.Native = &fakeNative{}
			err := f.m.EnsureRole(ctx, role)
			if !errors.Is(err, ErrInvalidRoute) || fresh != 0 {
				t.Fatalf("wake must reject locally without blocking other mail: fresh=%d err=%v", fresh, err)
			}
		})
	}
}

func TestWakeRegisteredRootDoesNotDependOnSnapshotOrFleetRead(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.m.Prepare(ctx, Target{Project: f.proj, BeadID: "example-app-t11", Title: "Repair"}); err != nil {
		t.Fatal(err)
	}
	f.m.Numbered = func(context.Context) ([]tui.NumberedWorkstream, error) {
		t.Fatal("registered wake read the fleet snapshot")
		return nil, nil
	}
	f.m.Fresh = func(context.Context) ([]tui.NumberedWorkstream, error) {
		t.Fatal("registered wake rescanned fleet")
		return nil, nil
	}
	native := &fakeNative{}
	f.m.Native = native
	if err := f.m.EnsureRole(ctx, "manager-example-app-t11"); err != nil {
		t.Fatal(err)
	}
	if !native.started["manager-example-app-t11"] {
		t.Fatal("registered owner not resumed")
	}
}

func TestWakeRevalidatesRegisteredParentBeforeStarting(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.m.Prepare(ctx, Target{Project: f.proj, BeadID: "example-app-t11"}); err != nil {
		t.Fatal(err)
	}
	f.bc.ParentsResult = map[string]string{"example-app-t11": "real-root", "real-root": ""}
	native := &fakeNative{}
	f.m.Native = native
	if err := f.m.EnsureRole(ctx, "manager-example-app-t11"); !errors.Is(err, ErrInvalidRoute) {
		t.Fatalf("moved root was accepted: %v", err)
	}
	if len(native.started) != 0 {
		t.Fatal("stale registration started a Manager")
	}
}
