// Package wake nudges agents when mail arrives for them. It reads the inbox of
// the commander role, every manager-* role and every running operator-* role
// and, for mail the agent has not been shown yet, types a short system-sourced
// line into that agent's chat pane, opening the window first if it is missing.
//
// An inbox is read only when it may hold news: at first sight, when a sender
// signalled mail for the role (mailstate.Signal), while mail waits undelivered,
// or once per SweepEvery as a safety net. So mail-server calls scale with mail
// sent, not with the number of agents.
//
// Typing is the fallback, not the main path: Claude agents get unread mail
// injected by the `cmdr internal-mail-hook` hook at their next prompt or stop,
// which advances the mailstate cursor. The Waker only wakes for mail newer than
// that cursor, and never types into a pane the Probe says is busy.
package wake

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/mailstate"
	"github.com/example-user/commander/internal/store"
)

type Mailbox interface {
	FetchInbox(ctx context.Context, projectKey, agent string, since time.Time, opts agentmail.InboxOptions) ([]agentmail.Mail, error)
}

type Directory interface {
	ListAmAgents(ctx context.Context, amProjectKey string) ([]store.AmAgent, error)
	ListRunningRuns(ctx context.Context) ([]store.AgentRun, error)
}

// Deliverer types prompt into the chat pane of role, opening its window first.
type Deliverer interface {
	Deliver(ctx context.Context, role, prompt string) error
}

// StaleDeliverer is a Deliverer that can also handle mail that has waited too long:
// clear a stray keystroke from the input box, or report ErrStuck.
type StaleDeliverer interface {
	DeliverStale(ctx context.Context, role, prompt string) error
}

// StuckAfter is how long unread mail may wait on a non-empty input box before the
// Waker clears a stray keystroke or reports the agent as stuck.
const StuckAfter = 2 * time.Minute

// SweepEvery is how often an inbox nobody signalled is read anyway, for mail
// sent outside commander's senders.
const SweepEvery = 2 * time.Minute

// HostMailReportAfter is how long mail may wait on a running host Operator before
// the board says so: long enough for an ordinary long tool call to return.
const HostMailReportAfter = 20 * time.Minute

// BlockedError retains undeliverable mail while routing or ownership is repaired.
// Retrying is a slow revalidation, never authorization to transfer a session.
type BlockedError struct {
	Cause  error
	Remedy string
}

func (e *BlockedError) Error() string { return fmt.Sprintf("%v. Remedy: %s", e.Cause, e.Remedy) }
func (e *BlockedError) Unwrap() error { return e.Cause }

const retryInitial = 10 * time.Second
const retryMaximum = 2 * time.Minute
const blockedRecheck = 5 * time.Minute

type deliveryRetry struct {
	attempts int
	next     time.Time
	err      error
	batch    []agentmail.Mail // what failed: read since, nothing is left to hold back
}

type Waker struct {
	// OnStuck is called each poll (first=true only the first time for that mail) when mail has waited past StuckAfter and
	// the input box holds real text. The daemon marks the row and mails Commander.
	OnStuck func(role string, m agentmail.Mail, first bool)
	stuck   map[string]bool
	// Only, when set, limits polling to the roles it accepts: with Commander
	// the sole planner, retired Managers' inboxes are never read.
	Only func(role string) bool
	// OnDeliver reports delivery failures, including while a retry backs off.
	// Deferred mail is not reported as delivered; success or an empty pending
	// batch clears the health issue.
	OnDeliver func(role string, err error)
	// OnHistory gets, after every poll, the old mail held per role (see held) and
	// the held subjects per role that mention a decision answer; empty when none
	// is. It wakes nobody.
	OnHistory func(held map[string]int, answers map[string][]string)

	mail       Mailbox
	dir        Directory
	out        Deliverer
	projectKey string
	interval   time.Duration

	// seen[role] holds mail ids already handled (delivered or pre-existing).
	seen    map[string]map[string]bool
	started bool

	// history[role] is the mail that was already old at first sight of a role
	// with no cursor; held[role] how much of it is still in the inbox. It is
	// neither delivered nor read: only a cursor ends it. Rebuilt after a restart.
	history     map[string]map[string]bool
	held        map[string]int
	heldAnswers map[string][]string

	// fetchedAt[role] is when its inbox read last started; pending[role] means
	// unread mail is still waiting to be delivered.
	fetchedAt map[string]time.Time
	pending   map[string]bool
	retries   map[string]deliveryRetry

	mu       sync.Mutex
	inboxes  map[string][]agentmail.Mail // last read inbox per role, for Unread
	nameOf   map[string]string           // role -> am name, as last listed
	unreadOf func(ctx context.Context, f mailstate.Fetcher, role string) (int, error)

	// signals and signal read and write mailstate's per-role send signals;
	// now is the clock. Overridable in tests.
	signals    func() map[string]time.Time
	signal     func(role string)
	needsCheck func(role string) bool
	now        func() time.Time

	// isUnread and markRead consult the per-role mail cursor. Overridable in
	// tests; they default to the real mailstate files.
	isUnread  func(role string, m agentmail.Mail) bool
	hasCursor func(role string) bool
	markRead  func(role string, m agentmail.Mail) error
}

// replayWindow bounds how far back a newly seen role's mail still wakes it.
const replayWindow = 2 * time.Minute

func New(mail Mailbox, dir Directory, out Deliverer, projectKey string, interval time.Duration) *Waker {
	return &Waker{mail: mail, dir: dir, out: out, projectKey: projectKey, interval: interval, seen: map[string]map[string]bool{},
		history: map[string]map[string]bool{}, held: map[string]int{}, heldAnswers: map[string][]string{},
		fetchedAt: map[string]time.Time{}, pending: map[string]bool{}, retries: map[string]deliveryRetry{}, inboxes: map[string][]agentmail.Mail{},
		isUnread: mailstate.IsUnread, hasCursor: mailstate.HasCursor, markRead: mailstate.MarkRead, unreadOf: mailstate.Count,
		signals: mailstate.Signals, signal: func(role string) { mailstate.Signal(role) }, needsCheck: mailstate.NeedsCheck, now: time.Now}
}

// Unread counts role's unread mail from the inbox the last poll read, against
// its read cursor: no mail-server call. A role with no mailbox has none; a
// mailbox not read yet is an error (callers keep their last count).
func (w *Waker) Unread(ctx context.Context, role string) (int, error) {
	w.mu.Lock()
	mails, read := w.inboxes[role]
	_, registered := w.nameOf[role]
	listed := w.nameOf != nil
	w.mu.Unlock()
	if !read {
		if listed && !registered {
			return 0, nil
		}
		return 0, fmt.Errorf("wake: inbox of %s not read yet", role)
	}
	return w.unreadOf(ctx, mailstate.FetchFunc(func(context.Context, string) ([]agentmail.Mail, error) { return mails, nil }), role)
}

// receives reports whether role can take mail now: an Operator only while its
// run is running (a finished one's mail is dropped anyway).
func receives(role string, running map[string]bool) bool {
	if !Wakeable(role) {
		return false
	}
	if strings.HasPrefix(role, "operator-") {
		return running[strings.ReplaceAll(role, ".", "-")]
	}
	return true
}

// due reports whether role's inbox may hold news worth a mail-server call.
func (w *Waker) due(role string, signals map[string]time.Time, now time.Time) bool {
	last, read := w.fetchedAt[role]
	switch {
	case !read, w.pending[role]:
		return true
	case signals[role].After(last):
		return true
	}
	return now.Sub(last) >= SweepEvery
}

// Wakeable reports whether role is an agent chat we deliver prompts to.
func Wakeable(role string) bool {
	return role == "commander" || strings.HasPrefix(role, "manager-") || strings.HasPrefix(role, "operator-")
}

// Prompt is the text typed into the agent's chat. It is formatted as a system
// notice rather than prose, so it does not read as something the user said.
func Prompt(from, subject, role string) string {
	return fmt.Sprintf("[mail] %s: %s — run cmdr internal-inbox --agent %s", from, oneLine(subject), role)
}

// MaxInlineBody bounds the text MailPrompt puts in a prompt.
const MaxInlineBody = 4000

// MailPrompt is Prompt, but mail from the user carries its text: he wrote it to be
// read, and a pointer to the inbox is easy to skip. Others stay a pointer.
func MailPrompt(from, subject, body, role string) string {
	body = strings.TrimSpace(body)
	if from != "user" || body == "" {
		return Prompt(from, subject, role)
	}
	if len(body) > MaxInlineBody {
		cut := MaxInlineBody
		for cut > 0 && !utf8.RuneStart(body[cut]) {
			cut--
		}
		body = body[:cut] + fmt.Sprintf("… (truncated; full text: cmdr internal-inbox --agent %s)", role)
	}
	return fmt.Sprintf("[mail] %s: %s\n%s", from, oneLine(subject), body)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// Run polls until ctx is cancelled.
func (w *Waker) Run(ctx context.Context) {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		w.Poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Poll runs one cycle. At first sight of a role (a restart counts: seen is in
// memory) old mail on a role with no cursor is history: not announced, not read,
// and reported through OnHistory until a cursor passes it. A decision answer is
// never history: it goes in the normal batch at any age.
func (w *Waker) Poll(ctx context.Context) {
	agents, err := w.dir.ListAmAgents(ctx, w.projectKey)
	if err != nil {
		log.Printf("wake: list agents: %v", err)
		return
	}
	runs, err := w.dir.ListRunningRuns(ctx)
	if err != nil {
		log.Printf("wake: list running runs: %v", err)
		return
	}
	running := make(map[string]bool, len(runs))
	for _, r := range runs {
		running[strings.ReplaceAll(r.AgentName, ".", "-")] = true
	}
	nameToRole := make(map[string]string, len(agents))
	roleToName := make(map[string]string, len(agents))
	for _, a := range agents {
		nameToRole[a.AmName] = a.Role
		roleToName[a.Role] = a.AmName
	}
	w.mu.Lock()
	w.nameOf = roleToName
	w.mu.Unlock()
	signals := w.signals()
	for _, a := range agents {
		if !receives(a.Role, running) || (w.Only != nil && !w.Only(a.Role)) {
			w.forget(a.Role)
			continue
		}
		start := w.now()
		if retry, ok := w.retries[a.Role]; ok && start.Before(retry.next) && !w.provenRead(a.Role, retry.batch) {
			// Keep the failure visible, with no inbox read: unread mail stays on the
			// server, arrivals included. Signals do not reset the budget.
			if w.OnDeliver != nil {
				w.OnDeliver(a.Role, retry.err)
			}
			continue
		}
		if !w.due(a.Role, signals, start) {
			continue
		}
		mails, err := w.mail.FetchInbox(ctx, w.projectKey, a.AmName, time.Time{}, agentmail.InboxOptions{Limit: 50})
		if err != nil {
			log.Printf("wake: fetch inbox %s: %v", a.Role, err)
			continue
		}
		w.fetchedAt[a.Role] = start
		w.pending[a.Role] = false
		w.mu.Lock()
		w.inboxes[a.Role] = mails
		w.mu.Unlock()
		hasCursor := w.hasCursor(a.Role)
		seen, known := w.seen[a.Role]
		if !known {
			seen = map[string]bool{}
			w.seen[a.Role] = seen
			// First sight of a role: its existing mail is history, not news. A
			// role can inherit an old inbox (an adopted Manager keeps its am
			// name), so replaying everything would flood its chat. Only mail
			// from the last replayWindow counts, which still catches mail that
			// arrives between a role's registration and our next poll. The rest
			// is held, not read: it may be mail nobody ever saw. A decision answer
			// is the exception: the user is waiting on it, so it is news at any age.
			// Mail the role's read cursor still shows as unread is news even
			// if it arrived while cmdrd was down (an idle native chat never
			// reads it on its own).
			history := map[string]bool{}
			cutoff := start.Add(-replayWindow)
			for _, m := range mails {
				if !hasCursor && m.SentAt.Before(cutoff) && !mailstate.IsAnswer(m.Subject) {
					history[m.ID] = true
				}
			}
			w.history[a.Role] = history
		}
		// Every unread mail goes in as one message, not one after another.
		var batch, held []agentmail.Mail
		var heldAnswers []string
		for _, m := range mails {
			if seen[m.ID] {
				continue
			}
			if !hasCursor && strings.HasPrefix(a.Role, "operator-") && !w.isUnread(a.Role, m) {
				// From before this Operator's run (mailstate.MarkRunStart): an earlier
				// run's. Its hook says so in one line and nobody needs alerting.
				seen[m.ID] = true
				continue
			}
			// With no cursor IsUnread also goes false as mail ages, with nobody
			// having read it: only a cursor says the hook showed it to the agent.
			if hasCursor && !w.isUnread(a.Role, m) {
				seen[m.ID] = true
				continue
			}
			if !hasCursor && w.history[a.Role][m.ID] {
				held = append(held, m)
				if mailstate.MentionsAnswer(m.Subject) {
					heldAnswers = append(heldAnswers, oneLine(m.Subject))
				}
				continue
			}
			batch = append(batch, m)
		}
		w.held[a.Role], w.heldAnswers[a.Role] = len(held), heldAnswers
		if len(batch) == 0 {
			delete(w.retries, a.Role)
			if w.OnDeliver != nil {
				w.OnDeliver(a.Role, nil)
			}
			continue
		}
		lines := make([]string, len(batch))
		oldest := batch[0]
		for i, m := range batch {
			from := m.From
			if role, ok := nameToRole[from]; ok {
				from = role
			}
			lines[i] = MailPrompt(from, m.Subject, m.Body, a.Role)
			if !m.SentAt.IsZero() && (oldest.SentAt.IsZero() || m.SentAt.Before(oldest.SentAt)) {
				oldest = m
			}
		}
		if len(held) > 0 {
			// This delivery's cursor passes the held mail: the role hears of it here.
			lines = append(lines, mailstate.HistoryNote(len(held), a.Role))
		}
		prompt := strings.Join(lines, "\n")
		w.pending[a.Role] = true // cleared once delivered or dropped
		err = w.out.Deliver(ctx, a.Role, prompt)
		if errors.Is(err, ErrHostOperator) {
			// Its mail hook takes this at its next tool call. Nothing here can
			// hurry that, so only the sweep reads again. The send signalled the
			// hook; mail with no live signal (an outside sender, a cursor write
			// that failed after the hook's read) gets one here, and fetchedAt
			// moves past it so this signal does not make the role due.
			w.pending[a.Role] = false
			delete(w.retries, a.Role)
			if !w.needsCheck(a.Role) {
				w.signal(a.Role)
				w.fetchedAt[a.Role] = w.now()
			}
			w.reportHostMail(a.Role, oldest, len(batch), start)
			continue
		}
		if err != nil && errors.Is(err, ErrDeferred) && !oldest.SentAt.IsZero() && time.Since(oldest.SentAt) > StuckAfter {
			// Waiting too long on an input box with text in it: a stray
			// keystroke gets cleared; real text means "stuck", reported.
			if sd, ok := w.out.(StaleDeliverer); ok {
				err = sd.DeliverStale(ctx, a.Role, prompt)
				if errors.Is(err, ErrStuck) {
					w.reportStuck(a.Role, oldest)
					continue
				}
			}
		}
		if err != nil && !errors.Is(err, ErrDeferred) && !errors.Is(err, ErrStuck) && w.provenRead(a.Role, batch) {
			// A failed attempt can take minutes. Mail read in the meantime is not
			// undeliverable: nothing is left to retain, retry or alert about.
			log.Printf("wake: %d mail(s) for %s were read while delivery failed: %v", len(batch), a.Role, err)
			err = nil
		}
		if w.OnDeliver != nil {
			if errors.Is(err, ErrOperatorGone) {
				w.OnDeliver(a.Role, nil)
			} else if !errors.Is(err, ErrDeferred) && !errors.Is(err, ErrStuck) {
				w.OnDeliver(a.Role, err)
			}
		}
		if errors.Is(err, ErrOperatorGone) {
			// A finished Operator: nobody to wake. The bead state machine
			// tells its Manager; stop retrying.
			log.Printf("wake: dropping %d mail(s) for finished %s", len(batch), a.Role)
		} else if err != nil {
			if errors.Is(err, ErrDeferred) {
				// Mid-turn: the Stop hook delivers it, and it reads the inbox
				// only when signalled.
				w.signal(a.Role)
			} else {
				r := w.retries[a.Role]
				r.attempts++
				delay := retryInitial
				for n := 1; n < r.attempts && delay < retryMaximum; n++ {
					delay *= 2
				}
				if delay > retryMaximum {
					delay = retryMaximum
				}
				var blocked *BlockedError
				if errors.As(err, &blocked) {
					delay = blockedRecheck
				}
				r.next, r.err, r.batch = start.Add(delay), err, batch
				w.retries[a.Role] = r
				log.Printf("wake: deliver to %s: %v (unread retained; revalidate after %s)", a.Role, err, delay)
			}
			continue // retry next poll
		}
		w.pending[a.Role] = false
		delete(w.retries, a.Role)
		// Held mail too, or an old answer's cursor would stop below it: the note
		// above said it now counts as read.
		for _, m := range append(batch, held...) {
			seen[m.ID] = true
			// The agent was just pointed at its inbox: do not also inject the
			// same mail from the hook on its next prompt.
			if err := w.markRead(a.Role, m); err != nil {
				log.Printf("wake: mark read %s: %v", a.Role, err)
			}
		}
		if w.hasCursor(a.Role) {
			delete(w.held, a.Role)
			delete(w.heldAnswers, a.Role)
		}
	}
	w.started = true
	if w.OnHistory != nil {
		held := make(map[string]int, len(w.held))
		answers := map[string][]string{}
		for role, n := range w.held {
			if n > 0 {
				held[role] = n
			}
			if s := w.heldAnswers[role]; len(s) > 0 {
				answers[role] = s
			}
		}
		w.OnHistory(held, answers)
	}
}

// provenRead re-reads role's cursor and reports whether it shows all of batch
// read. No cursor proves nothing: mail also leaves IsUnread by aging past
// FirstSightWindow, with nobody having read it.
func (w *Waker) provenRead(role string, batch []agentmail.Mail) bool {
	if !w.hasCursor(role) || len(batch) == 0 {
		return false
	}
	for _, m := range batch {
		if w.isUnread(role, m) {
			return false
		}
	}
	return true
}

// reportHostMail says on the board when mail has waited on a running host
// Operator past HostMailReportAfter; until then the wait is ordinary.
func (w *Waker) reportHostMail(role string, oldest agentmail.Mail, n int, now time.Time) {
	if w.OnDeliver == nil {
		return
	}
	var err error
	if age := now.Sub(oldest.SentAt); !oldest.SentAt.IsZero() && age > HostMailReportAfter {
		err = fmt.Errorf("%d mail(s) unread for %d min (oldest: %q). A Claude host Operator takes mail at its next tool call, so it is in a long one or was started before it had the mail hook; a Codex Operator takes none (cmdr internal-mail --interrupt ends it)", n, int(age.Minutes()), oneLine(oldest.Subject))
	}
	w.OnDeliver(role, err)
}

// forget drops what the waker keeps for a role that can no longer take mail, and
// any report on it. It keeps seen and history: if the role comes back that is no
// first sight, so mail sent in the gap is news and old history stays history.
func (w *Waker) forget(role string) {
	if _, tracked := w.fetchedAt[role]; tracked && w.OnDeliver != nil {
		w.OnDeliver(role, nil)
	}
	delete(w.held, role)
	delete(w.heldAnswers, role)
	delete(w.fetchedAt, role)
	delete(w.pending, role)
	delete(w.retries, role)
	w.mu.Lock()
	delete(w.inboxes, role)
	w.mu.Unlock()
}

// reportStuck tells OnStuck once per mail (the row keeps being re-marked by the
// caller while the mail stays unread).
func (w *Waker) reportStuck(role string, m agentmail.Mail) {
	if w.OnStuck == nil {
		return
	}
	if w.stuck == nil {
		w.stuck = map[string]bool{}
	}
	key := role + "/" + m.ID
	first := !w.stuck[key]
	w.stuck[key] = true
	w.OnStuck(role, m, first)
}
