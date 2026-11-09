package evals

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tui"
	"github.com/example-user/commander/internal/wsmgr"
)

type routingNative struct {
	held   bool
	starts int
}

func (n *routingNative) Fresh(string) bool                          { return true }
func (n *routingNative) Send(context.Context, string, string) error { return nil }
func (n *routingNative) Start(context.Context, string) error {
	n.starts++
	if n.held {
		return wsmgr.ErrSessionHeld
	}
	return nil
}

// Parent changes and terminal ownership are tested with real Manager validation,
// a temporary store and fake Native/Beads boundaries. No live session is touched.
func TestRoutingProgressScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/routing-progress.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Role, Root, Moved string
		Attempts          []struct {
			Name, Parent, Want string
			Held               bool
			Starts             int
		}
		FailedRecoveryTasks int `json:"failed_recovery_tasks"`
		MilestoneDone       int `json:"milestone_done"`
		MilestoneTotal      int `json:"milestone_total"`
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMMANDER_HOME", t.TempDir())
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dir := t.TempDir()
	_, err = st.InsertProject(ctx, store.Project{Name: "sample", Path: dir, Type: "code"})
	if err != nil {
		t.Fatal(err)
	}
	bc := &beads.MemClient{EdgeParentsOnly: true}
	for _, id := range []string{scenario.Root, scenario.Moved} {
		bc.Seed(beads.Bead{ID: id, Status: "open", Labels: []string{"kind:objective"}})
	}
	native := &routingNative{}
	m := &wsmgr.Manager{Store: st, Beads: bc, Native: native, Reg: &agentmail.MockRegistrar{NextName: "FixtureFox"}, AmKey: "/fixture",
		Numbered: func(context.Context) ([]tui.NumberedWorkstream, error) {
			return []tui.NumberedWorkstream{{N: 1, Project: "sample", BeadID: scenario.Moved}}, nil
		}}
	for _, attempt := range scenario.Attempts {
		bc.Edges = nil
		if attempt.Parent != "" {
			bc.Edges = []beads.Dep{{IssueID: scenario.Moved, DependsOnID: attempt.Parent, Type: "parent-child"}}
		}
		native.held = attempt.Held
		err := m.EnsureRole(ctx, scenario.Role)
		switch attempt.Want {
		case "invalid":
			if !errors.Is(err, wsmgr.ErrInvalidRoute) {
				t.Fatalf("%s: %v", attempt.Name, err)
			}
		case "held":
			if !errors.Is(err, wsmgr.ErrSessionHeld) {
				t.Fatalf("%s: %v", attempt.Name, err)
			}
		case "ready":
			if err != nil {
				t.Fatalf("%s: %v", attempt.Name, err)
			}
		default:
			t.Fatalf("unknown outcome %s", attempt.Want)
		}
		if native.starts != attempt.Starts {
			t.Fatalf("%s: starts=%d want=%d", attempt.Name, native.starts, attempt.Starts)
		}
	}
	ws := livedata.SnapWorkstream{Steps: []livedata.SnapStep{{BeadID: "delivery", Status: "open"}}}
	for i := 0; i < scenario.FailedRecoveryTasks; i++ {
		ws.Steps[0].Children = append(ws.Steps[0].Children, livedata.SnapStep{Task: true, Status: "closed", OwnerState: "died"})
	}
	if got := livedata.DeliveryMilestones(ws); got.Done != scenario.MilestoneDone || got.Total != scenario.MilestoneTotal {
		t.Fatalf("recovery history inflated delivery: %+v", got)
	}
}
