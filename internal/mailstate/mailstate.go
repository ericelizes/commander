// Package mailstate tracks which mail an agent has already been shown.
//
// Mail itself lives on the agent-mail server. This package only keeps a tiny
// per-role cursor (the newest mail the agent has been shown) in
// $COMMANDER_HOME/mail-cursors/<role>.json, so three independent readers agree
// on what is unread: the Claude Code hook that injects mail into a turn, the
// Waker that decides whether an idle agent needs a nudge, and the unread badge.
package mailstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/example-user/commander/internal/agentmail"
)

// Fetcher returns the inbox of the agent with the given mail role.
type Fetcher interface {
	Inbox(ctx context.Context, role string) ([]agentmail.Mail, error)
}

// FetchFunc adapts a function to Fetcher.
type FetchFunc func(ctx context.Context, role string) ([]agentmail.Mail, error)

func (f FetchFunc) Inbox(ctx context.Context, role string) ([]agentmail.Mail, error) {
	return f(ctx, role)
}

// FirstSightWindow bounds what counts as unread for a role with no cursor yet.
// An adopted Manager inherits an old inbox; showing all of it as unread would
// flood its first turn, so only mail from the last few minutes is news.
const FirstSightWindow = 10 * time.Minute

// answerPrefixes start every subject mailNeedsResolution and workflowGateMail
// send: the user's answer to a card or a gate.
var answerPrefixes = []string{"[decision] ", "[workflow ", "the user approved:", "the user deferred:"}

// IsAnswer reports whether subject is the user's answer to a decision. An answer
// is never history: the subject is all the Waker reads, so this is the one test.
func IsAnswer(subject string) bool {
	for _, p := range answerPrefixes {
		if strings.HasPrefix(subject, p) {
			return true
		}
	}
	return false
}

// MentionsAnswer reports whether subject carries an answer's subject anywhere (a
// reply, a forward, "[no Manager] ..."). Such mail can be held, so the report names it.
func MentionsAnswer(subject string) bool {
	for _, p := range answerPrefixes {
		if strings.Contains(subject, p) {
			return true
		}
	}
	return false
}

// agesOut reports whether m counts as old history for role, which has no cursor.
// An answer must arrive at any age; its stable id makes a repeat safe.
func agesOut(m agentmail.Mail, cutoff time.Time) bool {
	return !IsAnswer(m.Subject) && m.SentAt.Before(cutoff)
}

// historyCutoff is the send time below which a cursorless role's mail is history.
// For most roles that is FirstSightWindow ago. An Operator's mail may wait out a
// long tool call at any age, but a same-name respawn inherits the inbox of its
// earlier runs, so its bound is its own run's start, less FirstSightWindow for
// mail sent while it was down. Zero (run start not recorded): nothing is history.
func historyCutoff(role string) time.Time {
	if !strings.HasPrefix(role, "operator-") {
		return Now().Add(-FirstSightWindow)
	}
	p, err := runStartPath(role)
	if err != nil {
		return time.Time{}
	}
	info, err := os.Stat(p)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime().Add(-FirstSightWindow)
}

// Now is the clock, overridable in tests.
var Now = time.Now

type cursor struct {
	ID     string    `json:"id"`
	SentAt time.Time `json:"sent_at"`
}

func home() string {
	if h := os.Getenv("COMMANDER_HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".commander")
}

func dir() string { return filepath.Join(home(), "mail-cursors") }

func path(role string) (string, error) {
	if role == "" || strings.ContainsAny(role, `/\`) || role == "." || role == ".." {
		return "", fmt.Errorf("invalid mail role %q", role)
	}
	return filepath.Join(dir(), role+".json"), nil
}

// readCursor returns ok=false when the role has no cursor yet.
func readCursor(role string) (c cursor, ok bool, err error) {
	p, err := path(role)
	if err != nil {
		return cursor{}, false, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return cursor{}, false, nil
	}
	if err != nil {
		return cursor{}, false, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return cursor{}, false, fmt.Errorf("parse %s: %w", p, err)
	}
	return c, true, nil
}

// newerThan reports whether m was sent after the cursor. Mail ids from the
// server are increasing integers; when both sides parse we compare those and
// fall back to send time otherwise.
func newerThan(m agentmail.Mail, c cursor) bool {
	mi, merr := strconv.ParseInt(m.ID, 10, 64)
	ci, cerr := strconv.ParseInt(c.ID, 10, 64)
	if merr == nil && cerr == nil {
		return mi > ci
	}
	return m.SentAt.After(c.SentAt)
}

func older(a, b agentmail.Mail) bool {
	ai, aerr := strconv.ParseInt(a.ID, 10, 64)
	bi, berr := strconv.ParseInt(b.ID, 10, 64)
	if aerr == nil && berr == nil {
		return ai < bi
	}
	return a.SentAt.Before(b.SentAt)
}

// Unread returns the mail for role newer than its cursor, oldest first.
func Unread(ctx context.Context, f Fetcher, role string) ([]agentmail.Mail, error) {
	c, ok, err := readCursor(role)
	if err != nil {
		return nil, err
	}
	mails, err := f.Inbox(ctx, role)
	if err != nil {
		return nil, err
	}
	return filterUnread(role, mails, c, ok), nil
}

func filterUnread(role string, mails []agentmail.Mail, c cursor, hasCursor bool) []agentmail.Mail {
	var cutoff time.Time
	if !hasCursor {
		cutoff = historyCutoff(role)
	}
	var out []agentmail.Mail
	for _, m := range mails {
		if hasCursor && !newerThan(m, c) {
			continue
		}
		if !hasCursor && agesOut(m, cutoff) {
			continue
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool { return older(out[i], out[j]) })
	return out
}

// IsUnread reports whether m is still unread for role, given a cursor read
// once per poll. Used by the Waker to skip mail the hook already delivered.
func IsUnread(role string, m agentmail.Mail) bool {
	c, ok, err := readCursor(role)
	if err != nil {
		return true // unreadable cursor: err toward waking
	}
	return len(filterUnread(role, []agentmail.Mail{m}, c, ok)) == 1
}

// HiddenByAge counts the mail filterUnread leaves out of a role with no cursor
// only for its age. Nobody has read it, and the role's first cursor will pass it.
func HiddenByAge(role string, mails []agentmail.Mail) int {
	if _, ok, err := readCursor(role); err != nil || ok {
		return 0
	}
	cutoff := historyCutoff(role)
	n := 0
	for _, m := range mails {
		if agesOut(m, cutoff) {
			n++
		}
	}
	return n
}

// HistoryNote tells role about n old mails it was not shown: the one line that
// stands in for them when its first cursor passes them.
func HistoryNote(n int, role string) string {
	if strings.HasPrefix(role, "operator-") {
		return fmt.Sprintf("[mail] %d older mail(s) from before this run were not shown and will not be shown again: read them with cmdr internal-inbox --agent %s", n, role)
	}
	return fmt.Sprintf("[mail] %d older mail(s) in your inbox were not shown and now count as read: run cmdr internal-inbox --agent %s", n, role)
}

// StampFresh gives a role with no cursor one that passes nothing (id 0, sent
// at now), so every mail it receives from now on is news: a new nested Manager
// registered before anything is routed to it never reads its first mail as
// history. A role with a cursor is left alone.
func StampFresh(role string, now time.Time) error {
	if _, ok, err := readCursor(role); err != nil || ok {
		return err
	}
	p, err := path(role)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(cursor{ID: "0", SentAt: now})
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// HasCursor reports whether role has a read cursor yet.
func HasCursor(role string) bool {
	_, ok, err := readCursor(role)
	return err == nil && ok
}

// Count is the number of unread mails for role.
func Count(ctx context.Context, f Fetcher, role string) (int, error) {
	mails, err := Unread(ctx, f, role)
	return len(mails), err
}

// MarkRead advances role's cursor to m. It never moves the cursor backwards.
func MarkRead(role string, m agentmail.Mail) error {
	p, err := path(role)
	if err != nil {
		return err
	}
	if c, ok, err := readCursor(role); err == nil && ok && !newerThan(m, c) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(cursor{ID: m.ID, SentAt: m.SentAt})
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// MarkAllRead advances role's cursor past every mail in mails.
func MarkAllRead(role string, mails []agentmail.Mail) error {
	var newest *agentmail.Mail
	for i := range mails {
		if newest == nil || older(*newest, mails[i]) {
			newest = &mails[i]
		}
	}
	if newest == nil {
		return nil
	}
	return MarkRead(role, *newest)
}

// Signals: every sender in commander touches $COMMANDER_HOME/mail-signals/<role>
// after a send, so readers can skip an inbox nobody wrote to instead of asking
// the mail server every few seconds. Mail sent some other way has no signal;
// the Waker's slow sweep still finds it.

func signalDir() string { return filepath.Join(home(), "mail-signals") }

// Signal records that mail was just sent to each role. Best effort: a missing
// signal only delays delivery to the next sweep.
func Signal(roles ...string) {
	now := time.Now()
	for _, role := range roles {
		if _, err := path(role); err != nil {
			continue
		}
		p := filepath.Join(signalDir(), role)
		if err := os.Chtimes(p, now, now); err == nil {
			continue
		}
		if err := os.MkdirAll(signalDir(), 0o755); err != nil {
			return
		}
		if err := os.WriteFile(p, nil, 0o644); err == nil {
			_ = os.Chtimes(p, now, now)
		}
	}
}

// Signals returns when mail was last signalled for each role, in one read of
// the signal directory.
func Signals() map[string]time.Time {
	entries, err := os.ReadDir(signalDir())
	if err != nil {
		return nil
	}
	out := make(map[string]time.Time, len(entries))
	for _, e := range entries {
		if info, err := e.Info(); err == nil && !e.IsDir() {
			out[e.Name()] = info.ModTime()
		}
	}
	return out
}

// SignaledAt is when mail was last signalled for role (zero: never).
func SignaledAt(role string) time.Time {
	if _, err := path(role); err != nil {
		return time.Time{}
	}
	info, err := os.Stat(filepath.Join(signalDir(), role))
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func checkedPath(role string) (string, error) {
	p, err := path(role)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(p, ".json") + ".checked", nil
}

// CheckedAt is when role's inbox was last read in full by the mail hook.
func CheckedAt(role string) (time.Time, bool) {
	p, err := checkedPath(role)
	if err != nil {
		return time.Time{}, false
	}
	info, err := os.Stat(p)
	if err != nil {
		return time.Time{}, false
	}
	return info.ModTime(), true
}

// MarkChecked records that role's inbox was read at t (when the read started).
func MarkChecked(role string, t time.Time) error {
	p, err := checkedPath(role)
	if err != nil {
		return err
	}
	return touch(p, t)
}

// NeedsCheck reports whether role may have mail its last full read did not
// see: never read, or signalled since. A read that just failed holds it back for
// ReadBackoff, so an outage costs one try per ReadBackoff, not one per tool call.
func NeedsCheck(role string) bool {
	c, ok := CheckedAt(role)
	if ok && !SignaledAt(role).After(c) {
		return false
	}
	p, err := readFailedPath(role)
	if err != nil {
		return true
	}
	info, err := os.Stat(p)
	return err != nil || Now().Sub(info.ModTime()) >= ReadBackoff
}

// ReadBackoff is how long a failed mail-hook read keeps the hook from trying again.
const ReadBackoff = time.Minute

func readFailedPath(role string) (string, error) {
	p, err := path(role)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(p, ".json") + ".read-failed", nil
}

// MarkReadFailed stamps a failed read of role's inbox at t. It leaves .checked
// alone: the mail is still to be read once the backoff has passed.
func MarkReadFailed(role string, t time.Time) error {
	p, err := readFailedPath(role)
	if err != nil {
		return err
	}
	return touch(p, t)
}

func runStartPath(role string) (string, error) {
	p, err := path(role)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(p, ".json") + ".run-start", nil
}

// MarkRunStart records that a run of Operator role started at t: mail from before
// it is an earlier run's (historyCutoff). Each spawn moves it forward.
func MarkRunStart(role string, t time.Time) error {
	p, err := runStartPath(role)
	if err != nil {
		return err
	}
	return touch(p, t)
}

func touch(p string, t time.Time) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		return err
	}
	return os.Chtimes(p, t, t)
}
