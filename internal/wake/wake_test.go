package wake

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/mailstate"
	"github.com/example-user/commander/internal/store"
)

// fakeDir lists agents; every operator-* role has a running run unless it is
// in finished.
type fakeDir struct {
	agents   []store.AmAgent
	finished map[string]bool
}

func (f *fakeDir) ListAmAgents(context.Context, string) ([]store.AmAgent, error) {
	return f.agents, nil
}

func (f *fakeDir) ListRunningRuns(context.Context) ([]store.AgentRun, error) {
	var runs []store.AgentRun
	for _, a := range f.agents {
		if strings.HasPrefix(a.Role, "operator-") && !f.finished[a.Role] {
			runs = append(runs, store.AgentRun{AgentName: a.Role, Status: "running"})
		}
	}
	return runs, nil
}

type fakeMail map[string][]agentmail.Mail

func (f fakeMail) FetchInbox(_ context.Context, _, agent string, _ time.Time, _ agentmail.InboxOptions) ([]agentmail.Mail, error) {
	return f[agent], nil
}

// countingMail is fakeMail that counts inbox reads per am name.
type countingMail struct {
	fakeMail
	calls map[string]int
}

func (c *countingMail) FetchInbox(ctx context.Context, key, agent string, since time.Time, o agentmail.InboxOptions) ([]agentmail.Mail, error) {
	c.calls[agent]++
	return c.fakeMail.FetchInbox(ctx, key, agent, since, o)
}

func (c *countingMail) total() int {
	n := 0
	for _, v := range c.calls {
		n += v
	}
	return n
}

type fakeOut struct {
	got      [][2]string
	fail     bool
	deferred bool
}

func (f *fakeOut) Deliver(_ context.Context, role, prompt string) error {
	if f.deferred {
		return fmt.Errorf("%w: turn in progress", ErrDeferred)
	}
	if f.fail {
		return errors.New("no window")
	}
	f.got = append(f.got, [2]string{role, prompt})
	return nil
}

// readSet is an in-memory stand-in for the mailstate cursor files.
type readSet map[string]bool

type testWaker struct {
	*Waker
	read readSet
}

func newTestWaker(mail Mailbox, dir Directory, out Deliverer) *testWaker {
	w := New(mail, dir, out, "proj", time.Second)
	read := readSet{}
	w.isUnread = func(role string, m agentmail.Mail) bool { return !read[role+"/"+m.ID] }
	w.hasCursor = func(string) bool { return false }
	w.markRead = func(role string, m agentmail.Mail) error { read[role+"/"+m.ID] = true; return nil }
	// Unless a test drives signals itself, every role is signalled before each
	// poll, so every poll reads every inbox.
	w.signals = func() map[string]time.Time {
		all := map[string]time.Time{}
		if d, ok := dir.(*fakeDir); ok {
			for _, a := range d.agents {
				all[a.Role] = time.Now()
			}
		}
		return all
	}
	w.signal = func(string) {}
	return &testWaker{Waker: w, read: read}
}

func TestPoll_DeferredDeliveryRetriesQuietlyUntilSafe(t *testing.T) {
	dir := &fakeDir{agents: []store.AmAgent{{Role: "commander", AmName: "A"}}}
	mail := fakeMail{}
	out := &fakeOut{}
	w := newTestWaker(mail, dir, out)
	w.Poll(context.Background())

	mail["A"] = []agentmail.Mail{{ID: "1", From: "x", Subject: "s"}}
	out.deferred = true
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	w.Poll(context.Background())
	w.Poll(context.Background())
	if len(out.got) != 0 || logs.Len() != 0 {
		t.Fatalf("deferral must neither deliver nor log: got %v, logs %q", out.got, logs.String())
	}
	if w.read["commander/1"] {
		t.Fatal("deferred mail must stay unread")
	}
	out.deferred = false
	w.Poll(context.Background())
	if len(out.got) != 1 {
		t.Fatalf("expected delivery once safe, got %v", out.got)
	}
}

// If the Claude hook already put the mail in front of the agent (cursor
// advanced), the Waker must not type it again.
func TestPoll_DoesNotWakeForMailTheHookAlreadyDelivered(t *testing.T) {
	dir := &fakeDir{agents: []store.AmAgent{{Role: "manager-p", AmName: "A"}}}
	mail := fakeMail{}
	out := &fakeOut{}
	w := newTestWaker(mail, dir, out)
	w.hasCursor = func(string) bool { return true }
	w.Poll(context.Background())

	mail["A"] = []agentmail.Mail{{ID: "1", From: "c", Subject: "s"}}
	w.read["manager-p/1"] = true // hook ran between the mail landing and our poll
	w.Poll(context.Background())
	if len(out.got) != 0 {
		t.Fatalf("hook-delivered mail was typed again: %v", out.got)
	}
}

func TestPoll_WakeMarksMailReadSoTheHookDoesNotRepeatIt(t *testing.T) {
	dir := &fakeDir{agents: []store.AmAgent{{Role: "manager-p", AmName: "A"}}}
	mail := fakeMail{}
	out := &fakeOut{}
	w := newTestWaker(mail, dir, out)
	w.Poll(context.Background())

	mail["A"] = []agentmail.Mail{{ID: "1", From: "c", Subject: "s"}}
	w.Poll(context.Background())
	if len(out.got) != 1 || !w.read["manager-p/1"] {
		t.Fatalf("got %v, read %v", out.got, w.read)
	}
}

func TestPrompt_ReadsAsSystemNotice(t *testing.T) {
	got := Prompt("manager-x", "Plan\nready", "commander")
	if got != "[mail] manager-x: Plan ready — run cmdr internal-inbox --agent commander" {
		t.Fatal(got)
	}
	if strings.Contains(got, "You have mail") {
		t.Fatal("must not read like the user wrote it")
	}
}

func TestPoll_AnnouncesOnlyMailArrivingAfterStartup(t *testing.T) {
	dir := &fakeDir{agents: []store.AmAgent{{Role: "manager-commander", AmName: "CalmCave"}, {Role: "commander", AmName: "RainyBridge"}}}
	mail := fakeMail{"CalmCave": {{ID: "1", From: "RainyBridge", Subject: "old"}}}
	out := &fakeOut{}
	w := newTestWaker(mail, dir, out)

	w.Poll(context.Background())
	if len(out.got) != 0 {
		t.Fatalf("history must not wake: %v", out.got)
	}

	mail["CalmCave"] = append(mail["CalmCave"], agentmail.Mail{ID: "2", From: "RainyBridge", Subject: "new\nthing"})
	w.Poll(context.Background())
	w.Poll(context.Background())
	// The old mail stays out, but this delivery's cursor passes it: one line says so.
	want := "[mail] commander: new thing — run cmdr internal-inbox --agent manager-commander\n" + mailstate.HistoryNote(1, "manager-commander")
	if len(out.got) != 1 || out.got[0] != [2]string{"manager-commander", want} {
		t.Fatalf("got %v, want one prompt %q", out.got, want)
	}
}

func TestPoll_RoleRegisteredAfterStartupAnnouncesRecentMail(t *testing.T) {
	dir := &fakeDir{}
	mail := fakeMail{"BoldFox": {{ID: "7", From: "x", Subject: "hi", SentAt: time.Now()}}}
	out := &fakeOut{}
	w := newTestWaker(mail, dir, out)
	w.Poll(context.Background())

	dir.agents = []store.AmAgent{{Role: "operator-abc", AmName: "BoldFox"}}
	w.Poll(context.Background())
	if len(out.got) != 1 || out.got[0][0] != "operator-abc" {
		t.Fatalf("got %v", out.got)
	}
}

func TestPoll_FailedDeliveryRetries(t *testing.T) {
	dir := &fakeDir{agents: []store.AmAgent{{Role: "manager-p", AmName: "A"}}}
	mail := fakeMail{}
	out := &fakeOut{}
	w := newTestWaker(mail, dir, out)
	now := time.Now()
	w.now = func() time.Time { return now }
	w.Poll(context.Background())

	mail["A"] = []agentmail.Mail{{ID: "1", From: "c", Subject: "s"}}
	out.fail = true
	w.Poll(context.Background())
	out.fail = false
	now = now.Add(retryInitial)
	w.Poll(context.Background())
	if len(out.got) != 1 {
		t.Fatalf("expected retry to deliver once, got %v", out.got)
	}
}

func TestPoll_WakesCommanderToo(t *testing.T) {
	dir := &fakeDir{agents: []store.AmAgent{{Role: "commander", AmName: "A"}, {Role: "manager-p", AmName: "B"}}}
	mail := fakeMail{}
	out := &fakeOut{}
	w := newTestWaker(mail, dir, out)
	w.Poll(context.Background())
	mail["A"] = []agentmail.Mail{{ID: "1", From: "B", Subject: "needs you"}}
	w.Poll(context.Background())
	if len(out.got) != 1 || out.got[0][0] != "commander" || !strings.Contains(out.got[0][1], "[mail] manager-p: needs you") {
		t.Fatalf("got %v", out.got)
	}
}

func TestPoll_IgnoresNonAgentRoles(t *testing.T) {
	dir := &fakeDir{agents: []store.AmAgent{{Role: "dispatcher", AmName: "A"}}}
	mail := fakeMail{}
	out := &fakeOut{}
	w := newTestWaker(mail, dir, out)
	w.Poll(context.Background())
	mail["A"] = []agentmail.Mail{{ID: "1", From: "c", Subject: "s"}}
	w.Poll(context.Background())
	if len(out.got) != 0 {
		t.Fatalf("only commander/manager/operator roles are woken: %v", out.got)
	}
}

// An adopted Manager keeps its old inbox under a new role; the old mail must not
// be replayed into its chat, only mail from the last couple of minutes.
func TestPoll_NewRoleWithInheritedInboxOnlyAnnouncesFreshMail(t *testing.T) {
	dir := &fakeDir{}
	mail := fakeMail{"GoldTiger": {
		{ID: "1", From: "commander", Subject: "ancient", SentAt: time.Now().Add(-48 * time.Hour)},
		{ID: "2", From: "commander", Subject: "an hour ago", SentAt: time.Now().Add(-time.Hour)},
		{ID: "3", From: "commander", Subject: "You now own workstream", SentAt: time.Now().Add(-5 * time.Second)},
	}}
	out := &fakeOut{}
	w := newTestWaker(mail, dir, out)
	w.Poll(context.Background()) // startup, nothing registered yet

	dir.agents = []store.AmAgent{{Role: "manager-ws-1", AmName: "GoldTiger"}}
	w.Poll(context.Background())
	w.Poll(context.Background())
	if len(out.got) != 1 || !strings.Contains(out.got[0][1], "You now own workstream") {
		t.Fatalf("only the fresh adoption notice should wake it, got %v", out.got)
	}
}

func TestPoll_UnreadMailFromBeforeARestartStillWakes(t *testing.T) {
	dir := &fakeDir{agents: []store.AmAgent{{Role: "manager-x", AmName: "CalmCave"}}}
	mail := fakeMail{"CalmCave": {
		{ID: "1", From: "commander", Subject: "read already"},
		{ID: "2", From: "commander", Subject: "arrived while cmdrd was down"},
	}}
	out := &fakeOut{}
	w := newTestWaker(mail, dir, out)
	w.hasCursor = func(string) bool { return true }
	w.read["manager-x/1"] = true

	w.Poll(context.Background())
	if len(out.got) != 1 || out.got[0][1] != Prompt("commander", "arrived while cmdrd was down", "manager-x") {
		t.Fatalf("got %v", out.got)
	}
}

func TestPoll_MailForAFinishedOperatorIsDroppedNotRetried(t *testing.T) {
	dir := &fakeDir{agents: []store.AmAgent{{Role: "operator-x", AmName: "CalmCave"}}}
	mail := fakeMail{"CalmCave": nil}
	out := &goneOut{}
	w := newTestWaker(mail, dir, out)
	w.Poll(context.Background())
	mail["CalmCave"] = []agentmail.Mail{{ID: "5", From: "manager-y", Subject: "late"}}
	w.Poll(context.Background())
	w.Poll(context.Background())
	if out.calls != 1 {
		t.Fatalf("delivery attempts = %d, want 1 (then dropped)", out.calls)
	}
}

type goneOut struct{ calls int }

func (g *goneOut) Deliver(context.Context, string, string) error {
	g.calls++
	return fmt.Errorf("%w for role x", ErrOperatorGone)
}

func TestPoll_DeliversWaitingMailAsOneBatch(t *testing.T) {
	dir := &fakeDir{agents: []store.AmAgent{{Role: "commander", AmName: "C"}, {Role: "manager-p", AmName: "M"}}}
	mail := fakeMail{}
	out := &fakeOut{deferred: true}
	w := newTestWaker(mail, dir, out)
	w.Poll(context.Background())

	// Held (the user is talking to it) while three mails arrive...
	mail["C"] = []agentmail.Mail{{ID: "1", From: "M", Subject: "a"}, {ID: "2", From: "M", Subject: "b"}, {ID: "3", From: "x", Subject: "c"}}
	w.Poll(context.Background())
	w.Poll(context.Background())
	// ...then he pauses: all three go in as one message.
	out.deferred = false
	w.Poll(context.Background())
	want := "[mail] manager-p: a — run cmdr internal-inbox --agent commander\n" +
		"[mail] manager-p: b — run cmdr internal-inbox --agent commander\n" +
		"[mail] x: c — run cmdr internal-inbox --agent commander"
	if len(out.got) != 1 || out.got[0] != [2]string{"commander", want} {
		t.Fatalf("got %q", out.got)
	}
	if !w.read["commander/1"] || !w.read["commander/2"] || !w.read["commander/3"] {
		t.Fatalf("read = %v", w.read)
	}
	w.Poll(context.Background())
	if len(out.got) != 1 {
		t.Fatalf("delivered again: %q", out.got)
	}
}

func TestMailPrompt_InlinesUsersText(t *testing.T) {
	got := MailPrompt("user", "[from the user, via the app] hi", "hi there\nsecond line", "commander")
	if got != "[mail] user: [from the user, via the app] hi\nhi there\nsecond line" {
		t.Fatal(got)
	}
	if MailPrompt("manager-x", "Plan", "body", "commander") != Prompt("manager-x", "Plan", "commander") {
		t.Fatal("other senders stay a pointer")
	}
}

func TestMailPrompt_TruncatesLongBody(t *testing.T) {
	got := MailPrompt("user", "s", strings.Repeat("é", MaxInlineBody), "commander")
	if !strings.Contains(got, "(truncated; full text: cmdr internal-inbox --agent commander)") || !utf8.ValidString(got) {
		t.Fatal(got[len(got)-80:])
	}
}

// fleetWaker is a waker over n Managers and n running plus n finished
// Operators, with a fake clock and signals the test sends by hand.
func fleetWaker(t *testing.T, n int) (*testWaker, *countingMail, *fakeDir, map[string]time.Time, *time.Time) {
	t.Helper()
	dir := &fakeDir{finished: map[string]bool{}}
	mail := &countingMail{fakeMail: fakeMail{}, calls: map[string]int{}}
	for i := 0; i < n; i++ {
		dir.agents = append(dir.agents,
			store.AmAgent{Role: fmt.Sprintf("manager-p-%d", i), AmName: fmt.Sprintf("M%d", i)},
			store.AmAgent{Role: fmt.Sprintf("operator-live-%d", i), AmName: fmt.Sprintf("L%d", i)},
			store.AmAgent{Role: fmt.Sprintf("operator-done-%d", i), AmName: fmt.Sprintf("D%d", i)})
		dir.finished[fmt.Sprintf("operator-done-%d", i)] = true
	}
	w := newTestWaker(mail, dir, &fakeOut{})
	signals := map[string]time.Time{}
	clock := time.Unix(1_800_000_000, 0)
	w.signals = func() map[string]time.Time { return signals }
	w.now = func() time.Time { return clock }
	return w, mail, dir, signals, &clock
}

func TestPoll_FinishedOperatorsAreNotPolled(t *testing.T) {
	w, mail, _, _, _ := fleetWaker(t, 5)
	w.Poll(context.Background())
	for i := 0; i < 5; i++ {
		if c := mail.calls[fmt.Sprintf("D%d", i)]; c != 0 {
			t.Fatalf("finished operator D%d polled %d times", i, c)
		}
		if mail.calls[fmt.Sprintf("L%d", i)] != 1 || mail.calls[fmt.Sprintf("M%d", i)] != 1 {
			t.Fatalf("live roles must be read once at first sight: %v", mail.calls)
		}
	}
}

// With 40 receiving roles and no mail, polling every 5s costs one read per
// role at first sight and then nothing until the sweep: the call count does
// not grow with idle agents.
func TestPoll_IdleRolesCostNoCallsUntilSweep(t *testing.T) {
	w, mail, _, signals, clock := fleetWaker(t, 20) // 20 Managers + 20 live Operators
	w.Poll(context.Background())
	if mail.total() != 40 {
		t.Fatalf("first sight reads = %d, want 40", mail.total())
	}
	for i := 0; i < 20; i++ { // 100s of idle polling
		*clock = clock.Add(5 * time.Second)
		w.Poll(context.Background())
	}
	if mail.total() != 40 {
		t.Fatalf("idle polls read %d inboxes, want 0", mail.total()-40)
	}

	// One mail sent: exactly its recipient is read, on the next poll.
	*clock = clock.Add(5 * time.Second)
	signals["operator-live-3"] = *clock
	mail.fakeMail["L3"] = []agentmail.Mail{{ID: "9", From: "M0", Subject: "go"}}
	*clock = clock.Add(time.Second)
	w.Poll(context.Background())
	if mail.total() != 41 || mail.calls["L3"] != 2 {
		t.Fatalf("after one signal: total %d, L3 %d; want 41, 2", mail.total(), mail.calls["L3"])
	}
	w.Poll(context.Background())
	if mail.total() != 41 {
		t.Fatalf("a delivered signal must not be read again: %d", mail.total())
	}

	// The safety sweep reads each live role once per SweepEvery.
	*clock = clock.Add(SweepEvery)
	w.Poll(context.Background())
	if mail.total() != 81 {
		t.Fatalf("sweep total = %d, want 81", mail.total())
	}
}

func TestPoll_DeferredMailSignalsTheHookAndKeepsPolling(t *testing.T) {
	w, mail, _, signals, clock := fleetWaker(t, 1)
	out := &fakeOut{}
	w.out = out
	var signalled []string
	w.signal = func(role string) { signalled = append(signalled, role) }
	w.Poll(context.Background())

	signals["manager-p-0"] = clock.Add(time.Second)
	*clock = clock.Add(2 * time.Second)
	mail.fakeMail["M0"] = []agentmail.Mail{{ID: "1", From: "x", Subject: "s"}}
	out.deferred = true
	w.Poll(context.Background())
	*clock = clock.Add(5 * time.Second)
	w.Poll(context.Background())
	if mail.calls["M0"] != 3 || len(signalled) != 2 || signalled[0] != "manager-p-0" {
		t.Fatalf("waiting mail: reads %d, signals %v", mail.calls["M0"], signalled)
	}
	out.deferred = false
	*clock = clock.Add(5 * time.Second)
	w.Poll(context.Background())
	*clock = clock.Add(5 * time.Second)
	w.Poll(context.Background())
	if len(out.got) != 1 || mail.calls["M0"] != 4 {
		t.Fatalf("delivered %v after %d reads; want 1 delivery, 4 reads", out.got, mail.calls["M0"])
	}
}

func TestUnread_CountsFromTheLastPollWithoutCallingTheServer(t *testing.T) {
	w, mail, _, _, _ := fleetWaker(t, 1)
	mail.fakeMail["M0"] = []agentmail.Mail{{ID: "1"}, {ID: "2"}}
	w.unreadOf = func(ctx context.Context, f mailstate.Fetcher, role string) (int, error) {
		mails, _ := f.Inbox(ctx, role)
		return len(mails), nil
	}
	if _, err := w.Unread(context.Background(), "manager-p-0"); err == nil {
		t.Fatal("before any poll the count is unknown")
	}
	w.Poll(context.Background())
	before := mail.total()
	for i := 0; i < 10; i++ {
		if n, err := w.Unread(context.Background(), "manager-p-0"); err != nil || n != 2 {
			t.Fatalf("Unread = %d, %v", n, err)
		}
	}
	if n, err := w.Unread(context.Background(), "manager-unregistered"); err != nil || n != 0 {
		t.Fatalf("a role with no mailbox has no mail: %d, %v", n, err)
	}
	if mail.total() != before {
		t.Fatalf("Unread called the server %d times", mail.total()-before)
	}
}

type blockedOut struct {
	calls   int
	blocked bool
	prompts []string
}

func (o *blockedOut) Deliver(_ context.Context, role, prompt string) error {
	o.calls++
	if o.blocked {
		return &BlockedError{Cause: errors.New("terminal owns session"), Remedy: "release ownership or authorize handover"}
	}
	o.prompts = append(o.prompts, prompt)
	return nil
}

func TestPoll_BlockedOwnershipBackoffRetainsNewMailAndRecovers(t *testing.T) {
	dir := &fakeDir{agents: []store.AmAgent{{Role: "manager-p", AmName: "A"}}}
	mail := fakeMail{}
	out := &blockedOut{blocked: true}
	w := newTestWaker(mail, dir, out)
	now := time.Now()
	w.now = func() time.Time { return now }
	var health []error
	w.OnDeliver = func(_ string, err error) { health = append(health, err) }
	w.Poll(context.Background())
	mail["A"] = []agentmail.Mail{{ID: "1", Subject: "original", SentAt: now}}
	w.Poll(context.Background())
	mail["A"] = append(mail["A"], agentmail.Mail{ID: "2", Subject: "new during backoff", SentAt: now})
	for i := 0; i < 100; i++ {
		now = now.Add(time.Second)
		w.Poll(context.Background())
	}
	if out.calls != 1 || len(w.read) != 0 || !w.pending["manager-p"] || health[len(health)-1] == nil {
		t.Fatalf("calls=%d read=%v pending=%v health=%v", out.calls, w.read, w.pending, health)
	}
	// No timer authorizes a handover; ownership really changes before retry.
	out.blocked = false
	now = now.Add(blockedRecheck)
	w.Poll(context.Background())
	if out.calls != 2 || len(w.read) != 2 || len(out.prompts) != 1 || !strings.Contains(out.prompts[0], "original") || !strings.Contains(out.prompts[0], "new during backoff") || health[len(health)-1] != nil {
		t.Fatalf("calls=%d read=%v prompts=%v health=%v", out.calls, w.read, out.prompts, health)
	}
	w.Poll(context.Background())
	if out.calls != 2 {
		t.Fatal("replayed delivered batch")
	}
}

func TestPoll_FailureRetryDelayIsBounded(t *testing.T) {
	dir := &fakeDir{agents: []store.AmAgent{{Role: "manager-p", AmName: "A"}}}
	mail := fakeMail{}
	w := newTestWaker(mail, dir, &fakeOut{fail: true})
	now := time.Now()
	w.now = func() time.Time { return now }
	w.Poll(context.Background())
	mail["A"] = []agentmail.Mail{{ID: "1", SentAt: now}}
	for i := 0; i < 20; i++ {
		w.Poll(context.Background())
		r := w.retries["manager-p"]
		if d := r.next.Sub(now); d < retryInitial || d > retryMaximum {
			t.Fatalf("attempt %d delay %s", i, d)
		}
		now = r.next
	}
	if len(w.read) != 0 {
		t.Fatal("failed mail marked read")
	}
}

type readDuringFailure struct {
	during func()
}

func (o *readDuringFailure) Deliver(context.Context, string, string) error {
	o.during()
	return &BlockedError{Cause: errors.New("invalid route"), Remedy: "repair it"}
}

// The attempt that fails can take minutes; mail read while it ran must not raise
// a failure when it returns, nor be retried.
func TestPoll_MailReadDuringAFailedDeliveryRaisesNothing(t *testing.T) {
	dir := &fakeDir{agents: []store.AmAgent{{Role: "manager-gone", AmName: "A"}}}
	mail := fakeMail{}
	out := &readDuringFailure{}
	w := newTestWaker(mail, dir, out)
	now := time.Now()
	w.now = func() time.Time { return now }
	var health []error
	w.OnDeliver = func(_ string, err error) { health = append(health, err) }
	w.hasCursor = func(string) bool { return true }
	w.Poll(context.Background())
	mail["A"] = []agentmail.Mail{{ID: "1", Subject: "old", SentAt: now}}
	calls := 0
	out.during = func() { calls++; w.read["manager-gone/1"] = true }
	w.Poll(context.Background())
	for _, err := range health {
		if err != nil {
			t.Fatalf("read mail raised a failure: %v", health)
		}
	}
	if w.pending["manager-gone"] || len(w.retries) != 0 {
		t.Fatalf("pending=%v retries=%v", w.pending, w.retries)
	}
	w.Poll(context.Background())
	if calls != 1 {
		t.Fatalf("delivered %d times after the mail was read", calls)
	}
}

// Mail still unread after the failure stays retained and visible, including the
// part of a batch the reader did not reach.
func TestPoll_PartlyReadBatchStaysRetainedAndVisible(t *testing.T) {
	dir := &fakeDir{agents: []store.AmAgent{{Role: "manager-gone", AmName: "A"}}}
	mail := fakeMail{}
	out := &readDuringFailure{}
	w := newTestWaker(mail, dir, out)
	now := time.Now()
	w.now = func() time.Time { return now }
	var last error
	w.OnDeliver = func(_ string, err error) { last = err }
	w.hasCursor = func(string) bool { return true }
	w.Poll(context.Background())
	mail["A"] = []agentmail.Mail{{ID: "1", SentAt: now}, {ID: "2", SentAt: now}}
	out.during = func() { w.read["manager-gone/1"] = true }
	w.Poll(context.Background())
	if last == nil || !w.pending["manager-gone"] {
		t.Fatalf("unread mail lost its failure: last=%v pending=%v", last, w.pending)
	}
	if w.read["manager-gone/2"] {
		t.Fatal("unread mail marked read")
	}
}

// With no cursor, mail ages out of IsUnread with nobody reading it: only a cursor
// proves a read. A failed attempt that outlasts FirstSightWindow stays a failure.
func TestPoll_MailAgedPastFirstSightDuringAFailedDeliveryStaysAFailure(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	now := time.Now()
	mailstate.Now = func() time.Time { return now }
	t.Cleanup(func() { mailstate.Now = time.Now })
	const role = "manager-gone"
	mail := fakeMail{}
	out := &readDuringFailure{during: func() {}}
	w := New(mail, &fakeDir{agents: []store.AmAgent{{Role: role, AmName: "A"}}}, out, "proj", time.Second)
	w.now = func() time.Time { return now }
	var last error
	w.OnDeliver = func(_ string, err error) { last = err }
	w.Poll(context.Background())
	mail["A"] = []agentmail.Mail{{ID: "1", Subject: "unread", SentAt: now}}
	out.during = func() { now = now.Add(mailstate.FirstSightWindow + time.Minute) }
	mailstate.Signal(role)
	w.Poll(context.Background())
	if last == nil {
		t.Fatal("OnDeliver got no error: unread mail read as delivered")
	}
	if !w.pending[role] {
		t.Fatal("pending cleared for mail nobody read")
	}
	if len(w.retries) != 1 {
		t.Fatalf("retries=%v, want the failure kept", w.retries)
	}
	if mailstate.HasCursor(role) {
		t.Fatal("a cursor was written for mail nobody read")
	}
	// Every later poll keeps it, whatever its age: nobody has read it.
	for i := 0; i < 5; i++ {
		now = now.Add(blockedRecheck + time.Hour)
		w.Poll(context.Background())
		if last == nil || !w.pending[role] || len(w.retries) != 1 {
			t.Fatalf("poll %d: err=%v pending=%v retries=%v: unread mail read as delivered", i, last, w.pending[role], w.retries)
		}
		if w.seen[role]["1"] || mailstate.HasCursor(role) {
			t.Fatalf("poll %d: mail nobody read was marked seen or read", i)
		}
	}
}

// noCursorWaker is a Waker on the real mailstate cursors with old mail on a
// role that has none.
func noCursorWaker(t *testing.T, role string, mail fakeMail, out Deliverer, now *time.Time) (*Waker, *map[string]int) {
	t.Helper()
	dir := &fakeDir{agents: []store.AmAgent{{Role: role, AmName: "A"}}}
	mailstate.Now = func() time.Time { return *now }
	t.Cleanup(func() { mailstate.Now = time.Now })
	held := &map[string]int{}
	w := New(mail, dir, out, "proj", time.Second)
	w.now = func() time.Time { return *now }
	w.signals = func() map[string]time.Time { return map[string]time.Time{role: (*now).Add(time.Hour)} }
	w.signal = func(string) {}
	w.OnHistory = func(h map[string]int, _ map[string][]string) { *held = h }
	return w, held
}

// First sight of a role with no cursor: old mail neither wakes it nor reads as
// read. It stays held and reported, also after a restart forgets seen, until a
// cursor proves it read.
func TestPoll_OldMailAtFirstSightWithNoCursorIsHeldNotDropped(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	const role = "manager-inherited"
	now := time.Now()
	mail := fakeMail{"A": {
		{ID: "1", Subject: "ancient", SentAt: now.Add(-48 * time.Hour)},
		{ID: "2", Subject: "close it", SentAt: now.Add(-time.Hour)},
	}}
	out := &fakeOut{}
	w, held := noCursorWaker(t, role, mail, out, &now)
	for restart := 0; restart < 2; restart++ {
		for i := 0; i < 3; i++ {
			w.Poll(context.Background())
			if len(out.got) != 0 {
				t.Fatalf("restart %d: old mail woke the role: %v", restart, out.got)
			}
			if (*held)[role] != 2 {
				t.Fatalf("restart %d poll %d: held %v, want 2 on %s", restart, i, *held, role)
			}
			if mailstate.HasCursor(role) || w.seen[role]["1"] || w.seen[role]["2"] {
				t.Fatalf("restart %d: old mail read-marked with no evidence", restart)
			}
			now = now.Add(time.Hour)
		}
		w, held = noCursorWaker(t, role, mail, out, &now) // cmdrd restarted: seen is gone
	}
	w.Poll(context.Background())
	if err := mailstate.MarkAllRead(role, mail["A"]); err != nil { // someone read it
		t.Fatal(err)
	}
	w.Poll(context.Background())
	if len(*held) != 0 || len(out.got) != 0 {
		t.Fatalf("held %v, delivered %v after a cursor proved it read", *held, out.got)
	}
}

// A failure that outlives cmdrd: the restart must not turn it into history that
// nobody hears about.
func TestPoll_FailedMailAcrossARestartStaysVisible(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	const role = "manager-gone"
	now := time.Now()
	mail := fakeMail{}
	out := &readDuringFailure{during: func() {}}
	w, _ := noCursorWaker(t, role, mail, out, &now)
	w.Poll(context.Background())
	mail["A"] = []agentmail.Mail{{ID: "1", SentAt: now}}
	w.Poll(context.Background())

	now = now.Add(time.Hour)
	w, held := noCursorWaker(t, role, mail, out, &now)
	w.Poll(context.Background())
	if (*held)[role] != 1 || mailstate.HasCursor(role) {
		t.Fatalf("held %v cursor=%v: the failed mail vanished at the restart", *held, mailstate.HasCursor(role))
	}
}

// The bound: however much history, a new role gets one message, with its fresh
// mail plus one line counting the rest. That delivery tells it, so the cursor it
// writes over the old mail has evidence behind it.
func TestPoll_HistoryRidesFreshMailAsOneLine(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	const role = "manager-inherited"
	now := time.Now()
	var inbox []agentmail.Mail
	for i := 1; i <= 50; i++ {
		inbox = append(inbox, agentmail.Mail{ID: fmt.Sprint(i), Subject: "old", SentAt: now.Add(-time.Hour)})
	}
	mail := fakeMail{"A": append(inbox, agentmail.Mail{ID: "51", Subject: "You now own workstream", SentAt: now})}
	out := &fakeOut{}
	w, held := noCursorWaker(t, role, mail, out, &now)
	w.Poll(context.Background())
	w.Poll(context.Background())
	if len(out.got) != 1 {
		t.Fatalf("%d messages, want one: %v", len(out.got), out.got)
	}
	lines := strings.Split(out.got[0][1], "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "You now own workstream") || lines[1] != mailstate.HistoryNote(50, role) {
		t.Fatalf("prompt %q", out.got[0][1])
	}
	if len(*held) != 0 || !mailstate.HasCursor(role) {
		t.Fatalf("held %v cursor=%v after the role was told", *held, mailstate.HasCursor(role))
	}
}

// A role that stops taking mail leaves the report, and coming back does not
// replay what it held.
func TestPoll_FinishedOperatorLeavesTheReportAndReplaysNothing(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	const role = "operator-x"
	now := time.Now()
	mail := fakeMail{"A": {{ID: "1", SentAt: now.Add(-time.Hour)}}}
	out := &fakeOut{}
	dir := &fakeDir{agents: []store.AmAgent{{Role: role, AmName: "A"}}, finished: map[string]bool{}}
	w, held := noCursorWaker(t, role, mail, out, &now)
	w.dir = dir
	w.Poll(context.Background())
	if (*held)[role] != 1 {
		t.Fatalf("held %v", *held)
	}
	dir.finished[role] = true
	w.Poll(context.Background())
	if len(*held) != 0 {
		t.Fatalf("a finished Operator is still reported: %v", *held)
	}
	dir.finished[role] = false
	w.Poll(context.Background())
	if (*held)[role] != 1 || len(out.got) != 0 {
		t.Fatalf("held %v delivered %v", *held, out.got)
	}
}

// polling is a Waker on the real cursors and a clock the test moves, reading an
// inbox as the real one does: at first sight, when signalled, while pending and
// on the sweep.
func polling(t *testing.T, role string, mail Mailbox, out Deliverer, finished map[string]bool, now *time.Time) (*Waker, map[string]time.Time, *int) {
	t.Helper()
	mailstate.Now = func() time.Time { return *now }
	t.Cleanup(func() { mailstate.Now = time.Now })
	w := New(mail, &fakeDir{agents: []store.AmAgent{{Role: role, AmName: "A"}}, finished: finished}, out, "proj", time.Second)
	w.now = func() time.Time { return *now }
	signals := map[string]time.Time{}
	w.signals = func() map[string]time.Time { return signals }
	signalled := new(int)
	w.signal = func(string) { *signalled++ }
	return w, signals, signalled
}

// The user's answer, first seen long after it was sent (cmdrd was down), goes to
// the role whatever its age and is shown once, across a restart that lost the
// first attempt. The history around it stays one line and its cursor passes it.
func TestPoll_AnswerSeenFirstLongAfterSendingReachesTheRole(t *testing.T) {
	for _, subject := range []string{"[decision] sample-1.2: Merge", "[workflow approve] sample-1", "the user approved: Ship it", "the user deferred: Ship it"} {
		for name, age := range map[string]time.Duration{"3 hours": 3 * time.Hour, "90 days": 90 * 24 * time.Hour} {
			t.Run(subject+"/"+name, func(t *testing.T) {
				t.Setenv("COMMANDER_HOME", t.TempDir())
				const role = "manager-sample-1"
				now := time.Now()
				mail := fakeMail{"A": {
					{ID: "1", Subject: "ancient", SentAt: now.Add(-age - time.Hour)},
					{ID: "2", Subject: subject, SentAt: now.Add(-age)},
					{ID: "3", Subject: "later history", SentAt: now.Add(-age + time.Hour/2)},
				}}
				// The first cmdrd cannot start the Manager: nothing is lost.
				failing := &fakeOut{fail: true}
				w, _, _ := polling(t, role, mail, failing, nil, &now)
				var health error
				w.OnDeliver = func(_ string, err error) { health = err }
				w.Poll(context.Background())
				if health == nil || mailstate.HasCursor(role) {
					t.Fatalf("health %v cursor %v: the answer's failure is not visible", health, mailstate.HasCursor(role))
				}

				now = now.Add(time.Hour)
				out := &fakeOut{}
				w, _, _ = polling(t, role, mail, out, nil, &now) // restarted
				var held map[string]int
				w.OnHistory = func(h map[string]int, _ map[string][]string) { held = h }
				w.Poll(context.Background())
				if len(out.got) != 1 || !strings.Contains(out.got[0][1], subject) {
					t.Fatalf("answer did not reach the role: %v", out.got)
				}
				if !strings.Contains(out.got[0][1], mailstate.HistoryNote(2, role)) || strings.Contains(out.got[0][1], "ancient") || strings.Contains(out.got[0][1], "later history") {
					t.Fatalf("prompt %q", out.got[0][1])
				}
				if len(held) != 0 {
					t.Fatalf("held %v after the role was told", held)
				}

				// Shown once, and the history above the answer's id stays passed.
				for restart := 0; restart < 2; restart++ {
					w.Poll(context.Background())
					now = now.Add(SweepEvery)
					w.Poll(context.Background())
					out2 := &fakeOut{}
					w, _, _ = polling(t, role, mail, out2, nil, &now)
					w.Poll(context.Background())
					if len(out2.got) != 0 {
						t.Fatalf("repeated after a restart: %v", out2.got)
					}
				}
				if len(out.got) != 1 {
					t.Fatalf("repeated: %v", out.got)
				}
			})
		}
	}
}

// A mail that only mentions an answer is held like other history; the report
// carries its subject, so a lost answer does not sit inside a count.
func TestPoll_HeldMailMentioningAnAnswerIsNamed(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	const role = "manager-sample-1"
	now := time.Now()
	mail := fakeMail{"A": {
		{ID: "1", Subject: "Re: [decision] sample-1.2: Merge", SentAt: now.Add(-time.Hour)},
		{ID: "2", Subject: "ordinary", SentAt: now.Add(-time.Hour)},
	}}
	out := &fakeOut{}
	w, _, _ := polling(t, role, mail, out, nil, &now)
	var held map[string]int
	var answers map[string][]string
	w.OnHistory = func(h map[string]int, a map[string][]string) { held, answers = h, a }
	w.Poll(context.Background())
	if len(out.got) != 0 || held[role] != 2 {
		t.Fatalf("delivered %v held %v", out.got, held)
	}
	if got := answers[role]; len(got) != 1 || got[0] != "Re: [decision] sample-1.2: Merge" {
		t.Fatalf("answers %v", answers)
	}
}

type hostOperatorOut struct{ attempts int }

func (o *hostOperatorOut) Deliver(context.Context, string, string) error {
	o.attempts++
	return ErrHostOperator
}

// Mail on a running host Operator past FirstSightWindow: only the sweep reads it,
// nothing signals, and the board says so past the bound until the hook takes it.
func TestPoll_HostOperatorMailOnlyTheSweepReadsAndTheBoardReports(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	const role = "operator-x"
	now := time.Now()
	mail := &countingMail{fakeMail: fakeMail{}, calls: map[string]int{}}
	out := &hostOperatorOut{}
	w, signals, signalled := polling(t, role, mail, out, nil, &now)
	var health error
	w.OnDeliver = func(_ string, err error) { health = err }
	w.Poll(context.Background())

	sent := now
	mail.fakeMail["A"] = []agentmail.Mail{{ID: "1", Subject: "New direction", SentAt: sent}}
	signals[role] = now.Add(time.Second)
	reportedAt := time.Duration(0)
	const polls = 2 * 60 * 12 // two hours of 5s polls
	for i := 0; i < polls; i++ {
		now = now.Add(5 * time.Second)
		w.Poll(context.Background())
		if health != nil && reportedAt == 0 {
			reportedAt = now.Sub(sent)
		}
	}
	if max := polls/(int(SweepEvery/(5*time.Second))) + 2; mail.calls["A"] > max || out.attempts > max {
		t.Fatalf("fetches=%d attempts=%d in two hours, want at most %d (the sweep)", mail.calls["A"], out.attempts, max)
	}
	if *signalled != 0 {
		t.Fatalf("signalled %d times: that would read the inbox every poll", *signalled)
	}
	if w.pending[role] || w.seen[role]["1"] || mailstate.HasCursor(role) {
		t.Fatalf("pending=%v seen=%v cursor=%v", w.pending[role], w.seen[role]["1"], mailstate.HasCursor(role))
	}
	if reportedAt < HostMailReportAfter || reportedAt > HostMailReportAfter+SweepEvery+time.Minute {
		t.Fatalf("reported at %v, want just past %v", reportedAt, HostMailReportAfter)
	}
	if health == nil || !strings.Contains(health.Error(), "New direction") {
		t.Fatalf("report %v", health)
	}

	// The hook shows it: the cursor passes it, the report goes, and nothing repeats.
	if err := mailstate.MarkAllRead(role, mail.fakeMail["A"]); err != nil {
		t.Fatal(err)
	}
	attempts := out.attempts
	now = now.Add(SweepEvery)
	w.Poll(context.Background())
	if health != nil || out.attempts != attempts {
		t.Fatalf("health %v attempts %d->%d after the hook took it", health, attempts, out.attempts)
	}
}

// An Operator that ends takes its report with it: nobody is left to read that mail.
func TestPoll_FinishedOperatorClearsItsHostMailReport(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	const role = "operator-x"
	now := time.Now()
	finished := map[string]bool{}
	w, _, _ := polling(t, role, fakeMail{"A": {{ID: "1", SentAt: now}}}, &hostOperatorOut{}, finished, &now)
	var health error
	w.OnDeliver = func(_ string, err error) { health = err }
	w.Poll(context.Background())
	now = now.Add(HostMailReportAfter + SweepEvery)
	w.Poll(context.Background())
	if health == nil {
		t.Fatal("no report past the bound")
	}
	finished[role] = true
	w.Poll(context.Background())
	if health != nil {
		t.Fatalf("report %v stayed on a finished Operator", health)
	}
}

// An Operator back after a gap is no first sight: mail sent in the gap is news,
// however old by then, and what was already history stays history.
func TestPoll_OperatorBackAfterAGapGetsTheGapsMail(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	const role = "operator-x"
	now := time.Now()
	mail := fakeMail{"A": {{ID: "1", Subject: "old", SentAt: now.Add(-time.Hour)}}}
	finished := map[string]bool{}
	out := &fakeOut{}
	w, _, _ := polling(t, role, mail, out, finished, &now)
	w.Poll(context.Background())
	finished[role] = true
	w.Poll(context.Background())
	mail["A"] = append(mail["A"], agentmail.Mail{ID: "2", Subject: "sent in the gap", SentAt: now.Add(time.Minute)})
	now = now.Add(time.Hour)
	finished[role] = false
	w.Poll(context.Background())
	if len(out.got) != 1 || !strings.Contains(out.got[0][1], "sent in the gap") || strings.Contains(out.got[0][1], "[mail] A: old") {
		t.Fatalf("delivered %v", out.got)
	}
}

// While a retry backs off the inbox is not read, whatever signals arrive, and the
// kept error is still reported on every poll. The next read picks up what arrived.
func TestPoll_NoInboxReadWhileARetryBacksOff(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	const role = "manager-gone"
	now := time.Now()
	mail := &countingMail{fakeMail: fakeMail{"A": {{ID: "1", Subject: "unread", SentAt: now}}}, calls: map[string]int{}}
	out := &readDuringFailure{during: func() {}}
	w, signals, _ := polling(t, role, mail, out, nil, &now)
	var reports []error
	w.OnDeliver = func(_ string, err error) { reports = append(reports, err) }
	w.Poll(context.Background())
	if mail.calls["A"] != 1 || len(reports) != 1 || reports[0] == nil {
		t.Fatalf("calls=%v reports=%v", mail.calls, reports)
	}
	for i := 0; i < 10; i++ {
		now = now.Add(5 * time.Second)
		signals[role] = now
		w.Poll(context.Background())
	}
	if mail.calls["A"] != 1 {
		t.Fatalf("%d inbox reads during the backoff", mail.calls["A"])
	}
	if len(reports) != 11 {
		t.Fatalf("%d reports, want every poll", len(reports))
	}
	for _, err := range reports {
		var blocked *BlockedError
		if !errors.As(err, &blocked) {
			t.Fatalf("kept error lost: %v", reports)
		}
	}
	mail.fakeMail["A"] = append(mail.fakeMail["A"], agentmail.Mail{ID: "2", Subject: "arrived", SentAt: now})
	now = now.Add(blockedRecheck)
	w.Poll(context.Background())
	if mail.calls["A"] != 2 || !w.pending[role] {
		t.Fatalf("calls=%v pending=%v after the backoff", mail.calls, w.pending[role])
	}
}

// sweepWaker is a Waker on real cursors and real hook stamps; only the clock moves.
func sweepWaker(t *testing.T, role string, mail Mailbox, now *time.Time) (*Waker, *int) {
	t.Helper()
	w := New(mail, &fakeDir{agents: []store.AmAgent{{Role: role, AmName: "A"}}}, &hostOperatorOut{}, "proj", time.Second)
	w.now = func() time.Time { return *now }
	signalled := new(int)
	w.signal = func(r string) { *signalled++; mailstate.Signal(r) }
	return w, signalled
}

// Mail the sweep finds with no signal (an outside sender) is signalled once, for
// the hook, and that signal does not make the role due at every poll.
func TestPoll_SweepFoundMailWithNoSignalIsSignalledOnce(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	const role = "operator-x"
	now := time.Now()
	mail := &countingMail{fakeMail: fakeMail{}, calls: map[string]int{}}
	w, signalled := sweepWaker(t, role, mail, &now)
	w.Poll(context.Background())
	if err := mailstate.MarkChecked(role, time.Now()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	mail.fakeMail["A"] = []agentmail.Mail{{ID: "1", Subject: "outside", SentAt: time.Now()}}
	if mailstate.NeedsCheck(role) {
		t.Fatal("setup: nothing signalled yet")
	}

	now = now.Add(SweepEvery)
	w.Poll(context.Background())
	if !mailstate.NeedsCheck(role) || *signalled != 1 {
		t.Fatalf("needsCheck=%v signalled=%d: the hook would skip this mail", mailstate.NeedsCheck(role), *signalled)
	}
	reads := mail.calls["A"]
	for i := 0; i < 10; i++ {
		now = now.Add(5 * time.Second)
		w.Poll(context.Background())
	}
	if mail.calls["A"] != reads || *signalled != 1 {
		t.Fatalf("reads %d->%d signalled=%d: its own signal made the role due", reads, mail.calls["A"], *signalled)
	}
}

// The hook read the inbox but its cursor write failed: .checked moved and the mail
// is still unread. The next sweep signals again, so the mail is not stranded.
func TestPoll_FailedCursorWriteDoesNotStrandTheMail(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	const role = "operator-x"
	now := time.Now()
	mail := &countingMail{fakeMail: fakeMail{"A": {}}, calls: map[string]int{}}
	w, signalled := sweepWaker(t, role, mail, &now)
	w.Poll(context.Background())
	mail.fakeMail["A"] = []agentmail.Mail{{ID: "1", Subject: "direction", SentAt: time.Now()}}
	mailstate.Signal(role)
	time.Sleep(5 * time.Millisecond)
	// The hook's read: checked, then no cursor.
	if err := mailstate.MarkChecked(role, time.Now()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(SweepEvery)
	w.Poll(context.Background())
	if !mailstate.NeedsCheck(role) || *signalled != 1 {
		t.Fatalf("needsCheck=%v signalled=%d", mailstate.NeedsCheck(role), *signalled)
	}
}

// A hook-read batch does not keep its failure on the board through the retry window.
func TestPoll_RetryWindowEndsWhenTheHookReadTheBatch(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	const role = "manager-x"
	now := time.Now()
	batch := []agentmail.Mail{{ID: "1", Subject: "hi", SentAt: now}}
	out := &blockedOut{blocked: true}
	w := New(fakeMail{"A": batch}, &fakeDir{agents: []store.AmAgent{{Role: role, AmName: "A"}}}, out, "proj", time.Second)
	w.now = func() time.Time { return now }
	var health error
	w.OnDeliver = func(_ string, err error) { health = err }
	w.Poll(context.Background())
	if health == nil {
		t.Fatal("the failure was not reported")
	}
	if err := mailstate.MarkAllRead(role, batch); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute) // still inside blockedRecheck
	w.Poll(context.Background())
	if health != nil {
		t.Fatalf("read mail still on the board: %v", health)
	}
	if _, retrying := w.retries[role]; retrying {
		t.Fatal("still backing off for read mail")
	}
}

// A Codex Operator has no hook: its report stays up once raised, never flapping
// (one Commander alert on the board) across hours of sweeps, and it ends with the run.
func TestPoll_CodexOperatorReportNeverFlaps(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	const role = "operator-op-codex-sample-x1.2"
	now := time.Now()
	mail := fakeMail{"A": {}}
	finished := map[string]bool{}
	w, signals, _ := polling(t, role, mail, &hostOperatorOut{}, finished, &now)
	var transitions int
	var health error
	w.OnDeliver = func(_ string, err error) {
		if (err == nil) != (health == nil) {
			transitions++
		}
		health = err
	}
	w.Poll(context.Background())
	mail["A"] = []agentmail.Mail{{ID: "1", Subject: "stop now", SentAt: now}}
	signals[role] = now.Add(time.Second)
	for i := 0; i < 12*60*4; i++ { // four hours of 5s polls
		now = now.Add(5 * time.Second)
		w.Poll(context.Background())
	}
	if health == nil || transitions != 1 || mailstate.HasCursor(role) {
		t.Fatalf("health=%v transitions=%d cursor=%v", health, transitions, mailstate.HasCursor(role))
	}
	finished[role] = true
	now = now.Add(5 * time.Second)
	w.Poll(context.Background())
	if health != nil {
		t.Fatalf("report outlived the run: %v", health)
	}
}

// A restart (seen is memory) neither replays mail the hook already showed nor
// delivers again; a Codex Operator's old unread mail stays one held count.
func TestPoll_RestartReplaysNoReadMailOnAnOperator(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	const role = "operator-op-x-sample-x1.2"
	now := time.Now()
	old := agentmail.Mail{ID: "1", Subject: "direction", SentAt: now.Add(-time.Hour)}
	mail := fakeMail{"A": {old}}
	out := &hostOperatorOut{}

	if err := mailstate.MarkAllRead(role, mail["A"]); err != nil {
		t.Fatal(err)
	}
	w, _, _ := polling(t, role, mail, out, nil, &now)
	var health error
	w.OnDeliver = func(_ string, err error) { health = err }
	w.Poll(context.Background())
	if out.attempts != 0 || health != nil {
		t.Fatalf("read mail replayed: attempts=%d health=%v", out.attempts, health)
	}

	const codex = "operator-op-codex-sample-x1.3"
	cw, _, _ := polling(t, codex, mail, out, nil, &now)
	var held map[string]int
	cw.OnDeliver = func(_ string, err error) { health = err }
	cw.OnHistory = func(h map[string]int, _ map[string][]string) { held = h }
	cw.Poll(context.Background())
	if out.attempts != 0 || health != nil || held[codex] != 1 {
		t.Fatalf("attempts=%d health=%v held=%v", out.attempts, health, held)
	}
}

// A same-name respawn with no cursor: mail from before its run is neither news,
// nor a report, nor held; mail sent since is deferred to the hook as usual.
func TestPoll_RespawnedOperatorIgnoresItsEarlierRunsMail(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	const role = "operator-op-x-sample-x1.2"
	now := time.Now()
	if err := mailstate.MarkRunStart(role, now); err != nil {
		t.Fatal(err)
	}
	mail := fakeMail{"A": {
		{ID: "1", Subject: "the earlier run's", SentAt: now.Add(-mailstate.FirstSightWindow - time.Hour)},
		{ID: "2", Subject: "sent while it was down", SentAt: now.Add(-time.Minute)},
	}}
	out := &hostOperatorOut{}
	w, _, _ := polling(t, role, mail, out, nil, &now)
	var health error
	var held map[string]int
	w.OnDeliver = func(_ string, err error) { health = err }
	w.OnHistory = func(h map[string]int, _ map[string][]string) { held = h }
	w.Poll(context.Background())
	if out.attempts != 1 || health != nil || held[role] != 0 {
		t.Fatalf("attempts=%d health=%v held=%v", out.attempts, health, held)
	}
	n, err := mailstate.Count(context.Background(), mailstate.FetchFunc(func(context.Context, string) ([]agentmail.Mail, error) { return mail["A"], nil }), role)
	if err != nil || n != 1 {
		t.Fatalf("unread = %d, %v; want only the mail sent since", n, err)
	}
}
