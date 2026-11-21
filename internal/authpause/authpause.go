// Package authpause records a Claude account whose login failed (a refresh
// that no longer works, an expired OAuth session). While an account is
// paused no run is dispatched to it, and a step that asked for Claude waits
// for a sign-in instead of moving to another runtime. The state is one small
// file in the commander home, read by cmdrd's dispatch, the chat engine and
// the watcher that raises the single sign-in card.
package authpause

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ErrPaused is returned when dispatch is held for a Claude sign-in.
var ErrPaused = errors.New("Claude sign-in required")

// WaitingPrefix begins the execution error of a step waiting for a sign-in.
const WaitingPrefix = "Waiting for Claude sign-in: "

// Entry is one paused account.
type Entry struct {
	Account string `json:"account"`
	Since   int64  `json:"since"`
	Reason  string `json:"reason,omitempty"`
	// SawLoggedOut is set once the account was seen without a login after the
	// failure, so a later login is known to be a new sign-in.
	SawLoggedOut bool `json:"saw_logged_out,omitempty"`
	// CardProject and CardBead name the one step carrying the sign-in card.
	CardProject int64  `json:"card_project,omitempty"`
	CardBead    string `json:"card_bead,omitempty"`
}

var mu sync.Mutex

func path(home string) string { return filepath.Join(home, "chat", "auth-pause.json") }

func read(home string) map[string]Entry {
	out := map[string]Entry{}
	raw, err := os.ReadFile(path(home))
	if err != nil {
		return out
	}
	var list []Entry
	if json.Unmarshal(raw, &list) != nil {
		return out
	}
	for _, e := range list {
		if e.Account != "" {
			out[e.Account] = e
		}
	}
	return out
}

func write(home string, m map[string]Entry) error {
	list := make([]Entry, 0, len(m))
	for _, e := range m {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Account < list[j].Account })
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path(home)), 0o700); err != nil {
		return err
	}
	tmp := path(home) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path(home))
}

// Mark pauses account. It reports whether this call started the pause (a
// repeat keeps the first entry).
func Mark(home, account, reason string, now time.Time) (bool, error) {
	if account == "" {
		account = "default"
	}
	mu.Lock()
	defer mu.Unlock()
	m := read(home)
	if _, ok := m[account]; ok {
		return false, nil
	}
	if len(reason) > 300 {
		reason = reason[:300]
	}
	m[account] = Entry{Account: account, Since: now.Unix(), Reason: reason}
	return true, write(home, m)
}

// Active lists the paused accounts, by name.
func Active(home string) []Entry {
	mu.Lock()
	defer mu.Unlock()
	m := read(home)
	out := make([]Entry, 0, len(m))
	for _, e := range m {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Account < out[j].Account })
	return out
}

// Paused reports whether account is paused.
func Paused(home, account string) bool {
	if account == "" {
		account = "default"
	}
	mu.Lock()
	defer mu.Unlock()
	_, ok := read(home)[account]
	return ok
}

// Update rewrites one entry under the lock; a missing account is a no-op.
func Update(home, account string, f func(*Entry)) error {
	mu.Lock()
	defer mu.Unlock()
	m := read(home)
	e, ok := m[account]
	if !ok {
		return nil
	}
	f(&e)
	m[account] = e
	return write(home, m)
}

// Clear lifts the pause of account and returns the entry it removed.
func Clear(home, account string) (Entry, bool, error) {
	mu.Lock()
	defer mu.Unlock()
	m := read(home)
	e, ok := m[account]
	if !ok {
		return Entry{}, false, nil
	}
	delete(m, account)
	return e, true, write(home, m)
}

// Recovered reports whether the pause can lift: the account is logged in and
// either was seen logged out since the failure, its credential file changed
// after the pause began, or it is seen logged in and healthy (healthy is the
// caller's own probe of the account, e.g. `claude auth status`).
func Recovered(e Entry, loggedIn bool, credMTime time.Time, healthy bool) bool {
	if !loggedIn {
		return false
	}
	return e.SawLoggedOut || healthy || (!credMTime.IsZero() && credMTime.Unix() > e.Since)
}

// StructuredCode is the error code Claude Code puts on an assistant message
// when the login failed ("authentication_failed"). It is the only signal that
// pauses dispatch: message text is never read, since a run may legitimately
// print "401 unauthorized" or "not logged in" while it works.
const StructuredCode = "authentication_failed"

// IsFailureCode reports whether code is the runtime's structured auth failure.
func IsFailureCode(code string) bool { return code == StructuredCode }
