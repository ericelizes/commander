package evals

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/mailstate"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/wake"
	"github.com/example-user/commander/internal/wsmgr"
)

type staleRouteMail struct{ mails []agentmail.Mail }

func (m *staleRouteMail) FetchInbox(context.Context, string, string, time.Time, agentmail.InboxOptions) ([]agentmail.Mail, error) {
	return m.mails, nil
}

type staleRouteDir struct{ agents []store.AmAgent }

func (d staleRouteDir) ListAmAgents(context.Context, string) ([]store.AmAgent, error) {
	return d.agents, nil
}
func (staleRouteDir) ListRunningRuns(context.Context) ([]store.AgentRun, error) { return nil, nil }

// staleRouteOut fails like the real deliverer on an invalid route, and lets the
// mail get read while it "takes minutes".
type staleRouteOut struct {
	during func()
	calls  int
}

func (o *staleRouteOut) Deliver(context.Context, string, string) error {
	o.calls++
	o.during()
	return &wake.BlockedError{Cause: wsmgr.ErrInvalidRoute, Remedy: "repair it"}
}

// Real Manager validation, real mail cursors and the real Waker over a temp
// store and in-memory beads; no mail server, no Manager and no live state.
func TestStaleManagerRouteScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/stale-manager-route.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Beads []struct {
			ID, Status, Parent string
			Labels             []string
		}
		Senders []struct {
			Name, Role, Subject, To string
			Marked                  bool
		}
		StaleBatch struct {
			Role    string
			AmName  string `json:"am_name"`
			MailID  string `json:"mail_id"`
			Subject string
		} `json:"stale_batch"`
		NoCursor struct {
			Role     string
			AmName   string `json:"am_name"`
			MailID   string `json:"mail_id"`
			Subject  string
			HoursOld int `json:"hours_old"`
		} `json:"no_cursor"`
		OldAnswers []struct {
			Role     string
			AmName   string `json:"am_name"`
			MailID   string `json:"mail_id"`
			Subject  string
			HoursOld int `json:"hours_old"`
		} `json:"old_answers"`
		HostOperator struct {
			Role    string
			AmName  string `json:"am_name"`
			MailID  string `json:"mail_id"`
			Subject string
		} `json:"host_operator"`
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
	if _, err := st.InsertProject(ctx, store.Project{Name: "sample", Path: t.TempDir(), Type: "code"}); err != nil {
		t.Fatal(err)
	}
	bc := &beads.MemClient{EdgeParentsOnly: true}
	for _, b := range scenario.Beads {
		bc.Seed(beads.Bead{ID: b.ID, Status: b.Status, Labels: b.Labels})
		if b.Parent != "" {
			bc.Edges = append(bc.Edges, beads.Dep{IssueID: b.ID, DependsOnID: b.Parent, Type: "parent-child"})
		}
	}
	native := &routingNative{}
	m := &wsmgr.Manager{Store: st, Beads: bc, Native: native, AmKey: "/fixture"}

	for _, s := range scenario.Senders {
		t.Run(s.Name, func(t *testing.T) {
			var to, subject, body string
			sends := 0
			send := m.RoutedMail(func(_ context.Context, role, sub, b string) error {
				sends++
				to, subject, body = role, sub, b
				return nil
			})
			if err := send(ctx, s.Role, s.Subject, "the event"); err != nil {
				t.Fatal(err)
			}
			if sends != 1 || to != s.To || !strings.Contains(body, "the event") {
				t.Fatalf("sends=%d to=%s body=%q, want one mail to %s keeping its text", sends, to, body, s.To)
			}
			if s.Marked != strings.HasPrefix(subject, "[no Manager] ") {
				t.Fatalf("subject %q, marked=%v", subject, s.Marked)
			}
		})
	}
	if native.starts != 0 {
		t.Fatalf("routing started a Manager %d times", native.starts)
	}

	// The 06:00 shape: an attempt already under way when the mail is read.
	b := scenario.StaleBatch
	mail := &staleRouteMail{}
	out := &staleRouteOut{}
	w := wake.New(mail, staleRouteDir{agents: []store.AmAgent{{Role: b.Role, AmName: b.AmName}}}, out, "/fixture", time.Second)
	var failures []error
	w.OnDeliver = func(_ string, err error) {
		if err != nil {
			failures = append(failures, err)
		}
	}
	w.Poll(ctx)
	old := agentmail.Mail{ID: b.MailID, Subject: b.Subject, SentAt: time.Now()}
	mail.mails = []agentmail.Mail{old}
	out.during = func() {
		if err := mailstate.MarkRead(b.Role, old); err != nil {
			t.Fatal(err)
		}
	}
	mailstate.Signal(b.Role)
	w.Poll(ctx)
	if out.calls != 1 {
		t.Fatalf("attempts=%d, want the one attempt the read overlaps", out.calls)
	}
	if len(failures) != 0 {
		t.Fatalf("read mail raised an alert: %v", failures)
	}

	// Unread mail to the same role still fails visibly: it is never lost quietly.
	fresh := agentmail.Mail{ID: "42", Subject: "new notice", SentAt: time.Now()}
	mail.mails = []agentmail.Mail{old, fresh}
	out.during = func() {}
	mailstate.Signal(b.Role)
	w.Poll(ctx)
	var blocked *wake.BlockedError
	if len(failures) != 1 || !errors.As(failures[0], &blocked) {
		t.Fatalf("unread mail to a dead role must stay visible: %v", failures)
	}

	// A role with no cursor: old mail survives a restart and every poll, wakes
	// nobody, and is reported until something proves it read.
	n := scenario.NoCursor
	inherited := &staleRouteMail{mails: []agentmail.Mail{{ID: n.MailID, Subject: n.Subject, SentAt: time.Now().Add(-time.Duration(n.HoursOld) * time.Hour)}}}
	quiet := &staleRouteOut{during: func() {}}
	for restart := 0; restart < 2; restart++ {
		w := wake.New(inherited, staleRouteDir{agents: []store.AmAgent{{Role: n.Role, AmName: n.AmName}}}, quiet, "/fixture", time.Second)
		var held map[string]int
		w.OnHistory = func(h map[string]int, _ map[string][]string) { held = h }
		w.Poll(ctx)
		w.Poll(ctx)
		if held[n.Role] != 1 || quiet.calls != 0 || mailstate.HasCursor(n.Role) {
			t.Fatalf("restart %d: held=%v attempts=%d cursor=%v", restart, held, quiet.calls, mailstate.HasCursor(n.Role))
		}
	}

	// The user's answer on the same no-cursor role, however old, is not history:
	// it reaches the role, across a restart, once. The held mail rides as one line.
	for _, a := range scenario.OldAnswers {
		t.Run(a.Subject, func(t *testing.T) {
			t.Setenv("COMMANDER_HOME", t.TempDir())
			inbox := &staleRouteMail{mails: []agentmail.Mail{
				{ID: n.MailID, Subject: n.Subject, SentAt: time.Now().Add(-time.Duration(n.HoursOld) * time.Hour)},
				{ID: a.MailID, Subject: a.Subject, SentAt: time.Now().Add(-time.Duration(a.HoursOld) * time.Hour)},
			}}
			dir := staleRouteDir{agents: []store.AmAgent{{Role: a.Role, AmName: a.AmName}}}
			var prompts []string
			out := deliverFunc(func(_ context.Context, _, prompt string) error { prompts = append(prompts, prompt); return nil })
			for restart := 0; restart < 2; restart++ {
				w := wake.New(inbox, dir, out, "/fixture", time.Second)
				w.Poll(ctx)
				w.Poll(ctx)
			}
			if len(prompts) != 1 || !strings.Contains(prompts[0], a.Subject) || !strings.Contains(prompts[0], mailstate.HistoryNote(1, a.Role)) {
				t.Fatalf("prompts %q", prompts)
			}
		})
	}

	// A running host Operator: the deliverer defers to its hook, and the Waker reads
	// only on the sweep, says so past the bound and stops when the hook takes it.
	t.Run("host operator", func(t *testing.T) {
		t.Setenv("COMMANDER_HOME", t.TempDir())
		h := scenario.HostOperator
		sent := time.Now().Add(-wake.HostMailReportAfter - time.Minute)
		inbox := &staleRouteMail{}
		dir := staleRouteRunning{staleRouteDir{agents: []store.AmAgent{{Role: h.Role, AmName: h.AmName}}}}
		attempts := 0
		out := deliverFunc(func(context.Context, string, string) error { attempts++; return wake.ErrHostOperator })
		w := wake.New(inbox, dir, out, "/fixture", time.Second)
		var report error
		w.OnDeliver = func(_ string, err error) { report = err }
		w.Poll(ctx)
		inbox.mails = []agentmail.Mail{{ID: h.MailID, Subject: h.Subject, SentAt: sent}}
		mailstate.Signal(h.Role)
		time.Sleep(10 * time.Millisecond)
		w.Poll(ctx)
		for i := 0; i < 5; i++ {
			w.Poll(ctx)
		}
		if attempts != 1 || report == nil || !strings.Contains(report.Error(), h.Subject) {
			t.Fatalf("attempts=%d report=%v", attempts, report)
		}
	})
}

type deliverFunc func(ctx context.Context, role, prompt string) error

func (f deliverFunc) Deliver(ctx context.Context, role, prompt string) error {
	return f(ctx, role, prompt)
}

// staleRouteRunning lists a run for every agent.
type staleRouteRunning struct{ staleRouteDir }

func (d staleRouteRunning) ListRunningRuns(context.Context) ([]store.AgentRun, error) {
	var runs []store.AgentRun
	for _, a := range d.agents {
		runs = append(runs, store.AgentRun{AgentName: a.Role, Status: "running"})
	}
	return runs, nil
}
