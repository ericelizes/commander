package store

import (
	"context"
	"errors"
	"testing"
)

func TestPostgresOrchestrationModeIsSharedAndVersioned(t *testing.T) {
	st := postgresTestStore(t)
	ctx := context.Background()
	initial, e := st.OrchestrationControl(ctx)
	if e != nil || initial.Mode != "shadow" || initial.Revision != 1 {
		t.Fatalf("initial=%+v %v", initial, e)
	}
	active, e := st.SetOrchestrationControl(ctx, initial.Revision, "active", "verified cutover")
	if e != nil || active.Revision != 2 {
		t.Fatal(e)
	}
	reader, e := OpenPostgres(st.postgresDSN)
	if e != nil {
		t.Fatal(e)
	}
	defer reader.Close()
	if on, e := reader.ActiveOrchestration(ctx); e != nil || !on {
		t.Fatal("another process did not observe canonical mode", e)
	}
	if _, e = reader.SetOrchestrationControl(ctx, 1, "off", "stale caller"); !errors.Is(e, ErrJurisdictionConflict) {
		t.Fatalf("stale mode mutation=%v", e)
	}
	if _, e = reader.SetOrchestrationControl(ctx, 2, "invented", "bad mode"); e == nil {
		t.Fatal("unknown orchestration mode accepted")
	}
}
