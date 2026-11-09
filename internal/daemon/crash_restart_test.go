package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
)

type fullMail struct{ to, subject, body string }

// failingSpawner fails its first fails calls, then starts the Operator.
type failingSpawner struct {
	fails int
	reqs  []spawn.SpawnRequest
}

func (f *failingSpawner) Spawn(_ context.Context, req spawn.SpawnRequest) (*spawn.SpawnResult, error) {
	f.reqs = append(f.reqs, req)
	if len(f.reqs) <= f.fails {
		return nil, errors.New("could not read the workflow gate of p-ws.2 (busy), retry in a minute")
	}
	return &spawn.SpawnResult{AgentName: "operator-op-x"}, nil
}

func crashSettler(b *beads.MockClient, sp interface {
	Spawn(context.Context, spawn.SpawnRequest) (*spawn.SpawnResult, error)
}, mail *[]fullMail) runSettler {
	s := respawner(b, &fakeSpawner{}, new([]sentMail))
	s.spawner = sp
	s.now = func() time.Time { return time.Unix(1793214000, 0) }
	s.restarts = &restartBudget{}
	s.outputTail = func(store.AgentRun, string) string {
		return "assistant: running the suite\nresult/error: API Error: overloaded"
	}
	s.mail = func(_ context.Context, to, subject, body string) error {
		*mail = append(*mail, fullMail{to, subject, body})
		return nil
	}
	return s
}

// An Operator that dies on its own is started again once, on the same bead,
// branch and worktree, with the event written on the bead; nobody is mailed.
// When it dies again the step is the Manager's: marked died, mailed the facts.
func TestCrashRestartsOnceThenHandsTheStepBack(t *testing.T) {
	b := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "in_progress", Labels: []string{"owner:operator-x"}}}
	var mail []fullMail
	sp := &fakeSpawner{}
	if err := crashSettler(b, sp, &mail).died(context.Background(), run, false); err != nil {
		t.Fatal(err)
	}
	if len(sp.reqs) != 1 || !sp.reqs[0].Resume || sp.reqs[0].Branch != "op/x" || !strings.Contains(sp.reqs[0].ResumeNote, "final report") {
		t.Fatalf("spawn requests: %+v", sp.reqs)
	}
	calls := strings.Join(b.Calls, "\n")
	for _, want := range []string{"label=crash-respawn:1793214000|on=true", "SetStatus:p-ws.2.1|status=in_progress", "label=owner:operator-op-x|on=true", "AddComment:p-ws.2.1|text=cmdrd: operator-x ended without the process finishing (killed or crashed)"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing %q in:\n%s", want, calls)
		}
	}
	if strings.Contains(calls, beads.OperatorDiedLabel+"|on=true") || len(mail) != 0 {
		t.Fatalf("a restarted step is not the Manager's: mail %+v\n%s", mail, calls)
	}

	again := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "in_progress", Labels: []string{"owner:operator-x", "crash-respawn:1793214000"}}}
	if err := crashSettler(again, sp, &mail).died(context.Background(), run, false); err != nil {
		t.Fatal(err)
	}
	if len(sp.reqs) != 1 {
		t.Fatalf("restarted twice: %+v", sp.reqs)
	}
	calls = strings.Join(again.Calls, "\n")
	if !strings.Contains(calls, "label="+beads.OperatorDiedLabel+"|on=true") || !strings.Contains(calls, "not restarted") {
		t.Fatalf("calls:\n%s", calls)
	}
	if len(mail) != 1 || mail[0].to != "manager-p-ws" || !strings.Contains(mail[0].body, "Not restarted: it already died once") || !strings.Contains(mail[0].body, "Last output:\nassistant: running the suite\nresult/error: API Error: overloaded") {
		t.Fatalf("mail: %+v", mail)
	}
}

// A spawn that fails (a busy bead read) is tried once more at the next sweep;
// a second failure is the Manager's, with the error.
func TestCrashRestartSpawnFailureRetriesOnceThenMails(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fails   int
		spawns  int
		mailed  bool
		running bool
	}{
		{"second try starts it", 1, 2, false, true},
		{"second failure goes to the Manager", 2, 2, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "open"}}
			var mail []fullMail
			sp := &failingSpawner{fails: tc.fails}
			s := crashSettler(b, sp, &mail)
			var retries settleRetries
			s.settleReaped(context.Background(), spawn.HostReapResult{Died: []store.AgentRun{run}}, false, retries.add)
			if len(sp.reqs) != 1 || len(retries.pending) != 1 || len(mail) != 0 {
				t.Fatalf("first sweep: spawns %d, retries %d, mail %+v", len(sp.reqs), len(retries.pending), mail)
			}
			retries.run(context.Background())
			retries.run(context.Background())
			if len(sp.reqs) != tc.spawns || len(retries.pending) != 0 {
				t.Fatalf("spawns %d (want %d), retries left %d", len(sp.reqs), tc.spawns, len(retries.pending))
			}
			calls := strings.Join(b.Calls, "\n")
			if got := strings.Contains(calls, "label=owner:operator-op-x|on=true"); got != tc.running {
				t.Fatalf("claimed by the new Operator: %v\n%s", got, calls)
			}
			if (len(mail) == 1) != tc.mailed || (tc.mailed && !strings.Contains(mail[0].body, "starting it again failed twice: could not read the workflow gate")) {
				t.Fatalf("mail: %+v", mail)
			}
		})
	}
}

// Crash restarts are capped per step and in all per hour; the hour slides.
func TestCrashRestartBudget(t *testing.T) {
	var r restartBudget
	at := time.Unix(1793214000, 0)
	for i := 0; i < crashRestartsPerStep; i++ {
		if why := r.take("p-ws.2", at); why != "" {
			t.Fatalf("restart %d refused: %s", i, why)
		}
	}
	if why := r.take("p-ws.2", at); !strings.Contains(why, "3 Operators under p-ws.2") {
		t.Fatalf("fourth under one step: %q", why)
	}
	for i := 0; i < crashRestartsPerHour-crashRestartsPerStep; i++ {
		if why := r.take("p-ws."+string(rune('a'+i)), at); why != "" {
			t.Fatalf("restart elsewhere %d refused: %s", i, why)
		}
	}
	if why := r.take("p-other", at); !strings.Contains(why, "20 Operators were restarted") {
		t.Fatalf("over the fleet cap: %q", why)
	}
	if why := r.take("p-ws.2", at.Add(time.Hour)); why != "" {
		t.Fatalf("an hour later: %q", why)
	}
	var none *restartBudget
	if why := none.take("p-ws.2", at); why != "" {
		t.Fatalf("nil budget: %q", why)
	}
}

// A refused restart (the hour's budget is spent) hands the step back with the reason.
func TestCrashRestartOverBudgetMailsTheReason(t *testing.T) {
	b := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "open"}}
	var mail []fullMail
	sp := &fakeSpawner{}
	s := crashSettler(b, sp, &mail)
	for i := 0; i < crashRestartsPerStep; i++ {
		s.restarts.take("p-ws.2", s.now())
	}
	if err := s.died(context.Background(), run, false); err != nil {
		t.Fatal(err)
	}
	if len(sp.reqs) != 0 || len(mail) != 1 || !strings.Contains(mail[0].body, "Not restarted: 3 Operators under p-ws.2 were restarted in the last hour") {
		t.Fatalf("spawns %d, mail %+v", len(sp.reqs), mail)
	}
}

// cmdrd's own notes on a bead are never an Operator's final report.
func TestFinalReportSkipsCmdrdNotes(t *testing.T) {
	var sent []sentMail
	s := settler(&beads.MockClient{}, &sent)
	s.comments = func(context.Context, string, string) ([]beadComment, error) {
		return []beadComment{{Text: noteMark + "operator-x ended without a report; restarted", CreatedAt: "2030-01-01T00:00:00Z"}}, nil
	}
	if r := s.finalReport(context.Background(), run); r != "" {
		t.Fatalf("report %q", r)
	}
}

// A stop is the Manager's own act: the run is never restarted, and the step
// goes back to it, its mail naming who stopped it.
func TestStoppedRunIsNotRestarted(t *testing.T) {
	b := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "in_progress", Labels: []string{"owner:operator-x"}}}
	var mail []fullMail
	sp := &fakeSpawner{}
	s := crashSettler(b, sp, &mail)
	s.settleReaped(context.Background(), spawn.HostReapResult{Stopped: []spawn.StoppedRun{{Run: run, By: "manager-p-ws"}}}, false, func(func(context.Context) error) { t.Fatal("no retry expected") })
	if len(sp.reqs) != 0 {
		t.Fatalf("restarted a stopped run: %+v", sp.reqs)
	}
	calls := strings.Join(b.Calls, "\n")
	if !strings.Contains(calls, "label="+beads.OperatorDiedLabel+"|on=true") || !strings.Contains(calls, "stopped by manager-p-ws") {
		t.Fatalf("calls:\n%s", calls)
	}
	if len(mail) != 1 || mail[0].to != "manager-p-ws" || !strings.Contains(mail[0].subject, "stopped") || !strings.Contains(mail[0].body, "stopped by manager-p-ws") {
		t.Fatalf("mail: %+v", mail)
	}
	// The same after a wake: a stop is still a stop.
	sp.reqs, mail = nil, nil
	s.afterWake(context.Background(), spawn.HostReapResult{Stopped: []spawn.StoppedRun{{Run: run, By: "manager-p-ws"}}}, time.Hour, func(func(context.Context) error) { t.Fatal("no retry expected") })
	if len(sp.reqs) != 0 || len(mail) != 1 {
		t.Fatalf("after wake: reqs %+v mail %+v", sp.reqs, mail)
	}
}

// A respawn under the old name holds the step by the time the settle comes (a late
// retry): same owner label, but the settle hands nothing back and freezes nothing.
func TestStoppedSettleLeavesARespawnAlone(t *testing.T) {
	b := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "in_progress", Labels: []string{"owner:operator-x"}}}
	var mail []fullMail
	s := crashSettler(b, &fakeSpawner{}, &mail)
	s.lockWorktree = func(string) error { t.Error("froze the respawn's worktree"); return nil }
	newer := run
	newer.ID = run.ID + 1
	s.running = func(context.Context) ([]store.AgentRun, error) { return []store.AgentRun{newer}, nil }
	if err := s.stoppedOnRequest(context.Background(), spawn.StoppedRun{Run: run, By: "manager-p-ws"}); err != nil {
		t.Fatal(err)
	}
	if len(b.Calls) != 0 || len(mail) != 0 {
		t.Fatalf("calls %q mail %+v", b.Calls, mail)
	}
}

// Running runs it cannot read: the settle fails, so it retries, with nothing done.
func TestStoppedSettleRetriesAnUnreadableRead(t *testing.T) {
	b := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "in_progress", Labels: []string{"owner:operator-x"}}}
	var mail []fullMail
	s := crashSettler(b, &fakeSpawner{}, &mail)
	s.lockWorktree = func(string) error { t.Error("froze on an unreadable read"); return nil }
	s.running = func(context.Context) ([]store.AgentRun, error) { return nil, errors.New("busy") }
	if err := s.stoppedOnRequest(context.Background(), spawn.StoppedRun{Run: run, By: "manager-p-ws"}); err == nil {
		t.Fatal("want the read error")
	}
	if len(mail) != 0 {
		t.Fatalf("mail %+v", mail)
	}
}

// Nothing wired to read the running runs is an unreadable read: no freeze.
func TestFreezeWithNoRunningReaderFreezesNothing(t *testing.T) {
	b := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "in_progress", Labels: []string{"owner:operator-x"}}}
	var sent []sentMail
	s := settler(b, &sent)
	s.running = nil
	s.lockWorktree = func(string) error { t.Error("froze with no reader"); return nil }
	s.freeze(context.Background(), run)
}
