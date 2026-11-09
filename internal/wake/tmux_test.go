package wake

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/layout"
	"github.com/example-user/commander/internal/mailstate"
	"github.com/example-user/commander/internal/store"
)

type fakeManagers struct {
	roles []string
	err   error
}

func (f *fakeManagers) OpenByRole(_ context.Context, role string) (string, error) {
	f.roles = append(f.roles, role)
	return "%9", f.err
}

type noProjects struct{}

func (noProjects) ListProjects(context.Context) ([]store.Project, error) { return nil, nil }
func (noProjects) ListAgentRunsByProject(context.Context, int64, string) ([]store.AgentRun, error) {
	return nil, nil
}

func probeFor(f *fakePane) *Probe {
	return &Probe{Tmux: f.tmux}
}

func sentKeys(mr *layout.MockRunner) bool {
	for _, c := range mr.Calls {
		if len(c) > 0 && c[0] == "send-keys" {
			return true
		}
	}
	return false
}

func TestTmuxDeliverer_DefersWithoutTypingWhenPaneIsBusy(t *testing.T) {
	for name, scr := range map[string]string{
		"mid-turn":   screen(placeholder, busyFoot),
		"user typed": screen("half a sen", footer),
	} {
		t.Run(name, func(t *testing.T) {
			mr := &layout.MockRunner{}
			d := &TmuxDeliverer{Projects: noProjects{}, Director: layout.NewDirectorWith(mr), Managers: &fakeManagers{}, Probe: probeFor(&fakePane{screen: scr})}
			err := d.Deliver(context.Background(), "manager-x", "[mail] hi")
			if !errors.Is(err, ErrDeferred) {
				t.Fatalf("got %v, want ErrDeferred", err)
			}
			if sentKeys(mr) {
				t.Fatalf("typed into a busy pane: %v", mr.Calls)
			}
		})
	}
}

func TestTmuxDeliverer_TypesIntoIdleEmptyPane(t *testing.T) {
	mr := &layout.MockRunner{}
	d := &TmuxDeliverer{Projects: noProjects{}, Director: layout.NewDirectorWith(mr), Managers: &fakeManagers{}, Probe: probeFor(&fakePane{screen: screen(placeholder, footer)})}
	if err := d.Deliver(context.Background(), "manager-x", "[mail] hi"); err != nil {
		t.Fatal(err)
	}
	if !sentKeys(mr) {
		t.Fatal("idle pane with empty input should get the line")
	}
}

func TestTmuxDeliverer_ManagerRolesResolveThroughWorkstreamManagers(t *testing.T) {
	mgrs := &fakeManagers{}
	d := &TmuxDeliverer{Projects: noProjects{}, Director: layout.NewDirectorWith(&layout.MockRunner{}), Managers: mgrs, Probe: probeFor(&fakePane{screen: screen(placeholder, footer)})}
	if err := d.Deliver(context.Background(), "manager-example-app-t11", "hello"); err != nil {
		t.Fatal(err)
	}
	if len(mgrs.roles) != 1 || mgrs.roles[0] != "manager-example-app-t11" {
		t.Fatalf("roles = %v", mgrs.roles)
	}
}

func TestTmuxDeliverer_NoPerProjectAlias(t *testing.T) {
	mgrs := &fakeManagers{err: errors.New("no workstream Manager for role")}
	d := &TmuxDeliverer{Projects: noProjects{}, Director: layout.NewDirectorWith(&layout.MockRunner{}), Managers: mgrs}
	err := d.Deliver(context.Background(), "manager-example-app", "hello")
	if err == nil || !strings.Contains(err.Error(), "no workstream Manager") {
		t.Fatalf("a manager-<project> role must not resolve, got %v", err)
	}
	if (&TmuxDeliverer{Projects: noProjects{}, Director: layout.NewDirectorWith(&layout.MockRunner{})}).Deliver(context.Background(), "manager-x", "hi") == nil {
		t.Fatal("without a resolver there is nothing to deliver to")
	}
}

func TestDeliverStale_ClearsAStrayKeystrokeAndRestoresIt(t *testing.T) {
	mr := &layout.MockRunner{}
	clock := time.Unix(1000, 0)
	probe := &Probe{Tmux: (&fakePane{screen: screen("q", footer)}).tmux, Now: func() time.Time { return clock }}
	d := &TmuxDeliverer{Projects: noProjects{}, Director: layout.NewDirectorWith(mr), Managers: &fakeManagers{}, Probe: probe}
	if err := d.Deliver(context.Background(), "manager-x", "[mail] hi"); !errors.Is(err, ErrDeferred) {
		t.Fatalf("first look defers: %v", err)
	}
	clock = clock.Add(QuietPeriod + time.Second) // the 'q' has sat there untouched
	if err := d.DeliverStale(context.Background(), "manager-x", "[mail] hi"); err != nil {
		t.Fatal(err)
	}
	var seq []string
	for _, c := range mr.Calls {
		if len(c) > 0 && c[0] == "send-keys" {
			seq = append(seq, strings.Join(c[3:], " "))
		}
	}
	got := strings.Join(seq, "|")
	if got != "C-u|-l [mail] hi|Enter|-l q" {
		t.Fatalf("clear, send the wake line, put the stray back: %q", got)
	}
}

func TestDeliverStale_LeavesRealTextAloneAndReportsStuck(t *testing.T) {
	mr := &layout.MockRunner{}
	clock := time.Unix(1000, 0)
	probe := &Probe{Tmux: (&fakePane{screen: screen("please also check the migration plan", footer)}).tmux, Now: func() time.Time { return clock }}
	d := &TmuxDeliverer{Projects: noProjects{}, Director: layout.NewDirectorWith(mr), Managers: &fakeManagers{}, Probe: probe}
	_ = d.Deliver(context.Background(), "manager-x", "[mail] hi")
	clock = clock.Add(QuietPeriod + time.Second)
	if err := d.DeliverStale(context.Background(), "manager-x", "[mail] hi"); !errors.Is(err, ErrStuck) {
		t.Fatalf("got %v, want ErrStuck", err)
	}
	if sentKeys(mr) {
		t.Fatalf("a half-written message must not be touched: %v", mr.Calls)
	}
}

const (
	realTask = "sample-x1.17.8"
	realRole = "operator-op-narrow-check-sample-x1.17.8" // the AgentName a real spawn stores
)

// realRoles are the two spellings a real Operator's mail role arrives in.
var realRoles = []string{realRole, strings.ReplaceAll(realRole, ".", "-")}

type hostRuns struct{ pane sql.NullString }

func (hostRuns) ListProjects(context.Context) ([]store.Project, error) {
	return []store.Project{{ID: 1, Name: "p", Path: "/p"}}, nil
}
func (h hostRuns) ListAgentRunsByProject(context.Context, int64, string) ([]store.AgentRun, error) {
	return []store.AgentRun{{ProjectID: 1, TaskBeadID: realTask, AgentName: realRole, TmuxPaneID: h.pane, Status: "running"}}, nil
}

// A host Operator has a session name in its run row. Its mail is taken by the
// mail hook on its next turn: with no tmux server at all, delivery must defer,
// not fail with "commander tmux session not running", and touch no tmux.
func TestTmuxDeliverer_HostOperatorMailDefersToTheMailHook(t *testing.T) {
	mr := &layout.MockRunner{}
	mr.SetSessionExists("commander", false)
	d := &TmuxDeliverer{Projects: hostRuns{sql.NullString{String: "op-narrow-check-sample-x1-17-8", Valid: true}}, Director: layout.NewDirectorWith(mr), Managers: &fakeManagers{}}
	for _, role := range realRoles {
		for _, deliver := range []func(context.Context, string, string) error{d.Deliver, d.DeliverStale} {
			err := deliver(context.Background(), role, "[mail] hi")
			if !errors.Is(err, ErrHostOperator) || strings.Contains(err.Error(), "tmux session") {
				t.Fatalf("%s: got %v, want ErrHostOperator without tmux wording", role, err)
			}
		}
	}
	if len(mr.Calls) != 0 || sentKeys(mr) {
		t.Fatalf("host Operator delivery touched tmux: %v", mr.Calls)
	}
}

// A row from before host Operators is named by its task; any other role is gone.
func TestTmuxDeliverer_OldRowMatchesByTaskAndAStrangerIsGone(t *testing.T) {
	d := &TmuxDeliverer{Projects: hostRuns{sql.NullString{String: "op-x", Valid: true}}, Director: layout.NewDirectorWith(&layout.MockRunner{}), Managers: &fakeManagers{}}
	if err := d.Deliver(context.Background(), "operator-sample-x1-17-8", "[mail] hi"); !errors.Is(err, ErrHostOperator) {
		t.Fatalf("old row: got %v, want ErrHostOperator", err)
	}
	if err := d.Deliver(context.Background(), "operator-op-other-sample-x1.17.9", "[mail] hi"); !errors.Is(err, ErrOperatorGone) {
		t.Fatalf("stranger: got %v, want ErrOperatorGone", err)
	}
}

// The whole path on a real-shaped run row: a running Operator's mail is deferred
// to its hook, with no cursor, nothing marked seen and the mail still unread.
func TestPoll_RealShapedRunningOperatorKeepsItsMailForTheHook(t *testing.T) {
	for _, role := range realRoles {
		t.Run(role, func(t *testing.T) {
			t.Setenv("COMMANDER_HOME", t.TempDir())
			mail := fakeMail{"A": {}}
			mr := &layout.MockRunner{}
			out := &TmuxDeliverer{Projects: hostRuns{sql.NullString{String: "op-s", Valid: true}}, Director: layout.NewDirectorWith(mr), Managers: &fakeManagers{}}
			dir := &fakeDir{agents: []store.AmAgent{{Role: role, AmName: "A"}}}
			w := New(mail, dir, out, "proj", time.Second)
			w.Poll(context.Background())

			m := agentmail.Mail{ID: "7", Subject: "New direction", SentAt: time.Now()}
			mail["A"] = []agentmail.Mail{m}
			mailstate.Signal(role)
			time.Sleep(5 * time.Millisecond)
			w.Poll(context.Background())

			if err := out.Deliver(context.Background(), role, "x"); !errors.Is(err, ErrHostOperator) {
				t.Fatalf("Deliver: %v", err)
			}
			if mailstate.HasCursor(role) || w.seen[role]["7"] {
				t.Fatalf("mail marked read for the hook to miss: cursor=%v seen=%v", mailstate.HasCursor(role), w.seen[role]["7"])
			}
			n, err := mailstate.Count(context.Background(), mailstate.FetchFunc(func(context.Context, string) ([]agentmail.Mail, error) { return mail["A"], nil }), role)
			if err != nil || n != 1 {
				t.Fatalf("unread for the hook = %d, %v; want 1", n, err)
			}
			if len(mr.Calls) != 0 {
				t.Fatalf("touched tmux: %v", mr.Calls)
			}
		})
	}
}
