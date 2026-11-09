package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/wsmgr"
)

// recordingMailer is the am server: it records who a send names.
type recordingMailer struct {
	to      []string
	subject string
	opts    agentmail.SendOptions
}

func (*recordingMailer) EnsureProject(_ context.Context, key string) (string, error) { return key, nil }
func (*recordingMailer) RegisterAgent(context.Context, string, string, string, string) (string, error) {
	return "CmdrdFixture", nil
}
func (m *recordingMailer) SendMessage(_ context.Context, _, _ string, to []string, subject, _ string, opts agentmail.SendOptions) error {
	m.to, m.subject, m.opts = to, subject, opts
	return nil
}

// routeWorld seeds a temp store and in-memory beads from the stale-route scenario.
func routeWorld(t *testing.T, beadsOf []scenarioBead) (*wsmgr.Manager, *store.Store) {
	t.Helper()
	t.Setenv("COMMANDER_HOME", t.TempDir())
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.InsertProject(context.Background(), store.Project{Name: "sample", Path: t.TempDir(), Type: "code"}); err != nil {
		t.Fatal(err)
	}
	bc := &beads.MemClient{EdgeParentsOnly: true}
	for _, b := range beadsOf {
		bc.Seed(beads.Bead{ID: b.ID, Status: b.Status, Labels: b.Labels})
		if b.Parent != "" {
			bc.Edges = append(bc.Edges, beads.Dep{IssueID: b.ID, DependsOnID: b.Parent, Type: "parent-child"})
		}
	}
	return &wsmgr.Manager{Store: st, Beads: bc, AmKey: "/fixture"}, st
}

type scenarioBead struct {
	ID, Status, Parent string
	Labels             []string
}

// Lives here because checkPlans and the health board are private to cmdrd: the
// scenario's plan alerts run the real check, board, alert, route and role mail,
// over temp state and a recording mail server.
func TestPlanAlertScenario(t *testing.T) {
	raw, err := os.ReadFile("../../tests/evals/scenarios/stale-manager-route.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Beads      []scenarioBead
		PlanAlerts []struct {
			Name, Bead, To string
			Marked         bool
		} `json:"plan_alerts"`
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	if len(scenario.PlanAlerts) == 0 {
		t.Fatal("scenario has no plan alerts")
	}
	for _, s := range scenario.PlanAlerts {
		t.Run(s.Name, func(t *testing.T) {
			ctx := context.Background()
			m, st := routeWorld(t, scenario.Beads)
			if err := st.UpsertAmAgent(ctx, "/fixture", s.To, "ToFixture"); err != nil {
				t.Fatal(err)
			}
			mailer := &recordingMailer{}
			now := time.Unix(1000, 0)
			h := newHealthBoard(func() time.Time { return now })
			h.alert = healthAlert(routedRoleMail(m, st, mailer, "/fixture", "cmdrd", "urgent"))
			role := wsmgr.Role(s.Bead)
			snap := &livedata.Snapshot{
				Workstreams: []livedata.SnapWorkstream{{Project: "sample", BeadID: s.Bead, ManagerRole: role}},
				Display: &livedata.Display{Workstreams: []livedata.BoardRow{{Project: "sample", BeadID: s.Bead,
					Diagram: &livedata.Diagram{Findings: []livedata.DiagramFinding{{Check: "unheld", Bead: s.Bead + ".1", Why: "w", Fix: "f"}}}}}},
			}
			checkPlans(h, snap)
			now = now.Add(healthGrace + time.Second)
			checkPlans(h, snap)
			if got := h.list(); len(got) != 1 || got[0].Key != "plan:"+role {
				t.Fatalf("health board %v", got)
			}
			h.alertDue()
			if len(mailer.to) != 1 || mailer.to[0] != "ToFixture" {
				t.Fatalf("sent to %v, want %s", mailer.to, s.To)
			}
			if s.Marked != strings.HasPrefix(mailer.subject, "[no Manager] ") || !strings.Contains(mailer.subject, "[health] your plan reads wrong") {
				t.Fatalf("subject %q, marked=%v", mailer.subject, s.Marked)
			}
			if mailer.opts.Importance != "urgent" {
				t.Fatalf("importance %q", mailer.opts.Importance)
			}
		})
	}
}
