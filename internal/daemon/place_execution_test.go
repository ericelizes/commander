package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/place"
	"github.com/example-user/commander/internal/store"
)

func TestPostgresMacDiscovery(t *testing.T) {
	st, _ := pgtest.Open(t)
	ctx := context.Background()
	pid, err := st.InsertProject(ctx, store.Project{Name: "mac-discovery", Path: t.TempDir(), Type: "code", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,description,acceptance_criteria,created_at,updated_at) VALUES(?,'mac','Mac work','Read fixture','Report evidence','now','now')`, pid); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SetExecutionReady(ctx, pid, "mac", "commander", 1, 1, true, []string{"mac"}); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SetOrchestrationControl(ctx, 1, "active", "private discovery fixture"); err != nil {
		t.Fatal(err)
	}
	svc := place.NewService(place.Deps{Store: st})
	d := Daemon{store: st, places: svc}
	d.passPlaceExecutions(ctx, 2, 8, nil)
	dev := place.NewDevice("mac:discovery")
	if err = st.AddPlace(ctx, store.Place{Name: dev.Place, Kind: "mac", PublicKey: dev.Pub, PairedAt: 1, PairedBy: "fixture"}); err != nil {
		t.Fatal(err)
	}
	// A pairing added after startup is found without restarting a daemon.
	d.passPlaceExecutions(ctx, 2, 8, nil)
	runs, err := st.ListPlaceRuns(ctx, dev.Place, true)
	if err != nil || len(runs) != 1 {
		t.Fatal(runs, err)
	}
	if _, err = st.RevokePlace(ctx, dev.Place, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	d.passPlaceExecutions(ctx, 2, 8, nil)
	var enabled bool
	if err = st.DB().QueryRow(`SELECT enabled FROM execution_slots WHERE agent_id=?`, "place:"+dev.Place).Scan(&enabled); err != nil || enabled {
		t.Fatal("revoked slot remained enabled", enabled, err)
	}
	// Canonical acceptance still refuses an already offered revoked place.
	if ok, err := st.AcceptPlaceRun(ctx, runs[0].RunID, "fake", time.Now().Unix()); err != nil || ok {
		t.Fatal("revoked offer accepted", ok, err)
	}
}
