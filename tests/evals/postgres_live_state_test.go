package evals

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/example-user/commander/internal/api"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
)

func TestPostgresLiveStateScenario(t *testing.T) {
	var sc struct{ Workstream, Nested string }
	raw, e := os.ReadFile("scenarios/postgres-live-state.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &sc); e != nil {
		t.Fatal(e)
	}
	s, dsn := pgtest.Open(t)
	home, dir := t.TempDir(), t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	if e = os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pid, e := s.InsertProject(ctx, store.Project{Name: "native", Type: "code", Path: dir, Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	// Fixture creation stays private; the user's application is never started.
	beads.SetDoorServer(true)
	t.Cleanup(func() { beads.SetDoorServer(false) })
	c := beads.New()
	if e = c.Init(ctx, dir); e != nil {
		t.Fatal(e)
	}
	root, e := c.Create(ctx, dir, sc.Workstream, beads.CreateOptions{Labels: []string{"kind:objective", "kind:workstream"}, Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	nested, e := c.Create(ctx, dir, sc.Nested, beads.CreateOptions{Parent: root, Labels: []string{"kind:objective", "kind:workstream", "kind:track"}, Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	if e = beads.SetNestedManagers(dir, true); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{root, nested} {
		if e = s.UpsertWorkstreamManager(ctx, store.WorkstreamManager{ProjectID: pid, WorkstreamBeadID: id, SessionFile: "fixture"}); e != nil {
			t.Fatal(e)
		}
	}
	parentRole, childRole := store.WorkstreamManagerRole(root), store.WorkstreamManagerRole(nested)
	if e = s.SetAgentState(ctx, parentRole, store.AgentIdle, ""); e != nil {
		t.Fatal(e)
	}
	if e = s.SetAgentState(ctx, childRole, store.AgentWorking, "reviewing"); e != nil {
		t.Fatal(e)
	}
	if e = s.RecordRuntimePresence(ctx, map[int64]bool{}, time.Now(), []string{parentRole, childRole}); e != nil {
		t.Fatal(e)
	}
	source := &livedata.Source{Deps: livedata.Deps{Store: s, Beads: beads.ExportReads(beads.NewCached(c))}, Chats: func() map[string]livedata.ChatState {
		states, _ := s.ListAgentStates(context.Background())
		out := map[string]livedata.ChatState{}
		for _, a := range states {
			out[a.Role] = livedata.ChatState{State: a.State, Detail: a.Detail}
		}
		return out
	}, Live: func() ([]string, time.Time, bool) { return []string{}, time.Now(), true }}
	server := api.New(api.Options{Token: "fixture-token", Source: source.Snapshot, StateStore: s, Changes: s.WatchState(ctx), Interval: time.Hour})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	go server.Poll(ctx)
	conn, _, e := websocket.Dial(ctx, "ws"+httpServer.URL[4:]+"/api/events?token=fixture-token", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.CloseNow()
	read := func() livedata.Snapshot {
		t.Helper()
		_, raw, e := conn.Read(ctx)
		if e != nil {
			t.Fatal(e)
		}
		var frame struct {
			Snapshot livedata.Snapshot `json:"snapshot"`
		}
		if e = json.Unmarshal(raw, &frame); e != nil {
			t.Fatal(e)
		}
		return frame.Snapshot
	}
	first := read()
	if first.Degraded != nil || first.Storage != "postgres" || len(first.Workstreams) != 1 {
		t.Fatalf("initial state: %#v", first)
	}
	if first.Display.Workstreams[0].Working != 1 {
		t.Fatalf("nested Manager missing or idle parent counted: %#v", first.Display)
	}
	if e = s.SetAgentState(ctx, childRole, store.AgentIdle, ""); e != nil {
		t.Fatal(e)
	}
	for {
		next := read()
		if next.Version <= first.Version {
			continue
		}
		if len(next.Display.Workstreams) > 0 && next.Display.Workstreams[0].Working == 0 {
			break
		}
	}
	events, e := s.StateEventsSince(ctx, 0, 1000)
	if e != nil || len(events) == 0 {
		t.Fatalf("replay: %d %v", len(events), e)
	}
	cursor := events[len(events)-1].Seq
	if e = s.SetAgentState(ctx, childRole, store.AgentWorking, ""); e != nil {
		t.Fatal(e)
	}
	events, e = s.StateEventsSince(ctx, cursor, 1000)
	if e != nil || len(events) == 0 {
		t.Fatalf("resume replay: %d %v", len(events), e)
	}
	// A failed state source clears the snapshot, rather than retaining prior rows.
	failing := api.New(api.Options{Token: "fixture-token", StateStore: s, Source: func(context.Context) livedata.Snapshot {
		return livedata.Snapshot{Workstreams: first.Workstreams, KeptLastGood: true}
	}})
	failureServer := httptest.NewServer(failing.Handler())
	defer failureServer.Close()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, failureServer.URL+"/api/snapshot", nil)
	req.Header.Set("Authorization", "Bearer fixture-token")
	response, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	var unavailable livedata.Snapshot
	if e = json.NewDecoder(response.Body).Decode(&unavailable); e != nil {
		t.Fatal(e)
	}
	if response.StatusCode != http.StatusServiceUnavailable || len(unavailable.Workstreams) != 0 || unavailable.Degraded == nil {
		t.Fatalf("old rows were served: status=%d %#v", response.StatusCode, unavailable)
	}
}
