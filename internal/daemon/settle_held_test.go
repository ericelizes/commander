package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
)

// failingMem is a MemClient whose writes fail from the failAt-th one on (0 = never), and that
// checks the bead after every write that lands.
type failingMem struct {
	*beads.MemClient
	writes, failAt int
	after          func()
}

func (m *failingMem) write(do func() error) error {
	m.writes++
	if m.failAt > 0 && m.writes >= m.failAt {
		return errors.New("br update: killed")
	}
	if err := do(); err != nil {
		return err
	}
	m.after()
	return nil
}

func (m *failingMem) UpdateFields(ctx context.Context, dir, id string, f beads.Fields) error {
	return m.write(func() error { return m.MemClient.UpdateFields(ctx, dir, id, f) })
}

func (m *failingMem) SetLabel(ctx context.Context, dir, id, l string, on bool) error {
	return m.write(func() error { return m.MemClient.SetLabel(ctx, dir, id, l, on) })
}

func (m *failingMem) SetStatus(ctx context.Context, dir, id, st string) error {
	return m.write(func() error { return m.MemClient.SetStatus(ctx, dir, id, st) })
}

type scenarioSpawner struct{ fail bool }

func (s scenarioSpawner) Spawn(context.Context, spawn.SpawnRequest) (*spawn.SpawnResult, error) {
	if s.fail {
		return nil, errors.New("no capacity")
	}
	return &spawn.SpawnResult{AgentName: "operator-op-x"}, nil
}

// heldBy names what holds the bead: a wait, a live (new) Operator, or the untouched dead claim the
// settle has not written yet (its retry does). "" = nothing: the window the settle must never open.
func heldBy(b *beads.Bead) string {
	for _, l := range b.Labels {
		if w, ok := beads.ParseWaitLabel(l); ok {
			return "wait:" + w.Ref
		}
	}
	_, owner := beads.StepState(b.Status, b.Labels)
	switch {
	case owner == "operator-op-x" && b.Status == "in_progress":
		return "operator"
	case owner == "operator-x" && b.Status == "in_progress":
		return "not settled yet"
	}
	return ""
}

// TestSettleDiedHeldScenario runs scenarios/settle-died-held.json: every outcome, with a write
// failing at every point, never leaves the bead with no holder, and ends with the named one.
func TestSettleDiedHeldScenario(t *testing.T) {
	raw, err := os.ReadFile("../../tests/evals/scenarios/settle-died-held.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Bead struct {
			ID, Status string
			Labels     []string
		}
		Cases []struct {
			Name, Outcome          string
			Report, Restart, Spawn bool
			SpawnFails             bool   `json:"spawn_fails"`
			WantWait               string `json:"want_wait"`
			WantOwner              string `json:"want_owner"`
			WantStatus             string `json:"want_status"`
		}
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	if len(sc.Cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range sc.Cases {
		spawnFails := c.SpawnFails
		for failAt := 0; failAt <= 4; failAt++ {
			t.Run(c.Name+fmt.Sprintf("/fail-at-%d", failAt), func(t *testing.T) {
				mem := &beads.MemClient{}
				mem.Seed(beads.Bead{ID: sc.Bead.ID, Status: sc.Bead.Status, Labels: slices.Clone(sc.Bead.Labels)})
				fm := &failingMem{MemClient: mem, failAt: failAt}
				fm.after = func() {
					b, _ := mem.Show(context.Background(), "", sc.Bead.ID)
					if h := heldBy(b); h == "" {
						t.Fatalf("after write %d the bead has no holder: %s %v", fm.writes, b.Status, b.Labels)
					}
				}
				var sent []sentMail
				s := runSettler{projects: codeProject{}, beads: fm, running: noRunning, mail: func(_ context.Context, to, subject, _ string) error {
					sent = append(sent, sentMail{to, subject})
					return nil
				}, branchOf: func(string) (string, error) { return "op/x", nil }, now: func() time.Time { return time.Unix(1_800_000_000, 0) }}
				s.comments = func(context.Context, string, string) ([]beadComment, error) {
					if c.Report {
						return []beadComment{{Text: "Final report: done", CreatedAt: "2030-01-01T00:00:00Z"}}, nil
					}
					return nil, nil
				}
				if c.Spawn || spawnFails {
					s.spawner = scenarioSpawner{fail: spawnFails}
				}
				r := store.AgentRun{ID: 9, ProjectID: 3, TaskBeadID: sc.Bead.ID, AgentName: "operator-x"}
				var err error
				switch c.Outcome {
				case "exited":
					err = s.exited(context.Background(), r)
				case "died":
					err = s.died(context.Background(), r, c.Restart)
				case "stopped":
					err = s.stoppedOnRequest(context.Background(), spawn.StoppedRun{Run: r, By: "manager-p-ws"})
				}
				b, _ := mem.Show(context.Background(), "", sc.Bead.ID)
				if heldBy(b) == "" {
					t.Fatalf("ended with no holder (err %v): %s %v", err, b.Status, b.Labels)
				}
				if failAt > 0 {
					return // a failed write is retried at the next sweep; held is what counts here
				}
				var pe *partialErr
				if err != nil && !(spawnFails && errors.As(err, &pe)) {
					t.Fatal(err)
				}
				_, owner := beads.StepState(b.Status, b.Labels)
				if owner != c.WantOwner || b.Status != c.WantStatus {
					t.Fatalf("owner %q status %q, want %q %q: %v", owner, b.Status, c.WantOwner, c.WantStatus, b.Labels)
				}
				waits := beads.WaitsOf(b.Labels)
				if c.WantWait == "" && len(waits) != 0 || c.WantWait != "" && (len(waits) != 1 || waits[0].Ref != c.WantWait) {
					t.Fatalf("waits %+v, want %q", waits, c.WantWait)
				}
				if strings.Contains(strings.Join(b.Labels, " "), "owner:operator-x") {
					t.Fatalf("the dead claim stayed: %v", b.Labels)
				}
			})
		}
	}
}
