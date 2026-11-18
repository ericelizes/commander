package chat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentrt"
)

func TestLateCommanderSessionScenario(t *testing.T) {
	var scenario struct {
		Role         string `json:"role"`
		MissingError string `json:"missing_error"`
		Message      string `json:"message"`
		Cases        []struct {
			Owner string `json:"owner"`
			Start bool   `json:"start"`
		} `json:"cases"`
	}
	raw, err := os.ReadFile("../../tests/evals/scenarios/late-commander-session.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	for _, tc := range scenario.Cases {
		t.Run("owner="+tc.Owner, func(t *testing.T) {
			h := newHarness(t)
			t.Setenv("COMMANDER_HOME", h.home)
			t.Setenv("COMMANDER_WORKSPACE_ROOT", h.work)
			h.e.opts.Resolver = StoreResolver{Home: h.home}
			ctx := context.Background()
			if _, err := h.e.Start(ctx, scenario.Role, StartOptions{}); err == nil || !strings.Contains(err.Error(), scenario.MissingError) {
				t.Fatalf("early start: %v", err)
			}
			before, err := h.e.Status(scenario.Role)
			if err != nil || before.SessionID != "" || before.Running {
				t.Fatalf("early status: %+v %v", before, err)
			}
			id := agentrt.NewClaudeSessionID()
			if err := os.WriteFile(filepath.Join(h.home, "commander.claude.session-id"), []byte(id+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.Owner != "" {
				command := "claude --resume " + id
				if tc.Owner == "headless" {
					command = "claude -p --resume " + id
				}
				h.procs.set(id, []Proc{{PID: 4242, Command: command}})
			}
			// Advance the bounded ownership cache without a wall-clock sleep.
			h.e.mu.Lock()
			hit := h.e.ownerCache[scenario.Role]
			hit.at = time.Now().Add(-6 * time.Second)
			h.e.ownerCache[scenario.Role] = hit
			h.e.mu.Unlock()
			after, err := h.e.Status(scenario.Role)
			if err != nil || after.SessionID != id || after.Running || after.OwnedBy != tc.Owner || after.OwnerUnknown {
				t.Fatalf("late session status: %+v %v", after, err)
			}
			if tc.Start {
				started := h.start(scenario.Role)
				if started.SessionID != id || !started.Running {
					t.Fatalf("late start: %+v", started)
				}
				if err := h.e.Send(ctx, scenario.Role, scenario.Message); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := h.e.Start(ctx, scenario.Role, StartOptions{}); err == nil {
					t.Fatal("foreign session was started")
				}
			}
		})
	}
}
