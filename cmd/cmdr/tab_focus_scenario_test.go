package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
)

// Private-handler interaction eval runs the real tab CLI with an isolated
// backend. It never creates or mutates a decision card.
func TestInspectorFocusScenario(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var sc struct{ Decision, Window, Title string }
			raw, err := os.ReadFile("../../tests/evals/scenarios/inspector-focus.json")
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(raw, &sc); err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			t.Setenv("COMMANDER_HOME", home)
			var s *store.Store
			if backend == "postgres" {
				var dsn string
				s, dsn = pgtest.Open(t)
				if err = os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				s, err = store.Open(filepath.Join(home, "state.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { s.Close() })
			}
			ctx := context.Background()
			pid, err := s.InsertProject(ctx, store.Project{Name: "fixture", Type: "code", Path: t.TempDir(), Status: "active"})
			if err != nil {
				t.Fatal(err)
			}
			if err = s.UpsertWorkstreamManager(ctx, store.WorkstreamManager{ProjectID: pid, WorkstreamBeadID: sc.Window, SessionFile: "fixture"}); err != nil {
				t.Fatal(err)
			}
			old := tabCallerWS
			tabCallerWS = func() string { return sc.Window }
			t.Cleanup(func() { tabCallerWS = old })
			run := func() {
				t.Helper()
				if code, out, errOut := tab("add", "--decision", sc.Decision, "--title", sc.Title, "--focus"); code != 0 {
					t.Fatalf("focus: %d %s %s", code, out, errOut)
				}
			}
			run()
			id, err := s.GetActiveTab(ctx, pid, sc.Window)
			if err != nil || id == 0 {
				t.Fatalf("active: %d %v", id, err)
			}
			now := time.Unix(1700000000, 0)
			if err = s.SaveTabCache(ctx, pid, sc.Window, "card", now); err != nil {
				t.Fatal(err)
			}
			run() // Existing decision tab is refocused, preserving identity and cache.
			tabs, err := s.ListTabs(ctx, pid, sc.Window)
			if err != nil || len(tabs) != 1 {
				t.Fatalf("tabs: %+v %v", tabs, err)
			}
			got := tabs[0]
			if got.ID != id || got.Kind != "decision" || got.Target != sc.Decision || got.Title != sc.Title || got.ProjectID != pid || got.WorkstreamBeadID != sc.Window {
				t.Fatalf("custody: %+v", got)
			}
			if js, at, err := s.GetTabCache(ctx, pid, sc.Window); err != nil || js != "card" || !at.Equal(now) {
				t.Fatalf("cache: %q %v %v", js, at, err)
			}
		})
	}
}
