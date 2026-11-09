package decisions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RepeatAfter is how often a context that already holds the list gets it again.
const RepeatAfter = 30 * time.Minute

// ForRole is the active decisions role works under: all of them for
// Commander, the global ones plus its workstream's (and its tracks') for a
// Manager, none for anyone else.
func ForRole(ds []Decision, role string) []Decision {
	if role == "commander" {
		return ActiveIn(ds)
	}
	ws, ok := strings.CutPrefix(role, "manager-")
	if !ok || ws == "" {
		return nil
	}
	var out []Decision
	for _, d := range ActiveIn(ds) {
		if d.Scope == Global || d.Scope == ws || strings.HasPrefix(d.Scope, ws+".") {
			out = append(out, d)
		}
	}
	return out
}

// Text is the decisions as one turn's context.
func Text(ds []Decision) string {
	if len(ds) == 0 {
		return "The user's standing decisions: none in force now (added automatically)."
	}
	lines := make([]string, len(ds))
	for i, d := range ds {
		lines[i] = "- " + d.Line()
	}
	return "The user's standing decisions (added automatically; they hold until the user changes them, over your own defaults and earlier plans. " +
		"Never add a gate, wait, review or ask one of them rules out):\n" + strings.Join(lines, "\n")
}

type seen struct {
	Sig string `json:"sig"`
	At  int64  `json:"at"`
}

func seenPath(home, role string) string { return filepath.Join(Dir(home), "seen", role+".json") }

// MarkFull makes role's next turn carry the list again: its context no longer
// holds it (a new session, a resume, a compaction).
func MarkFull(home, role string) { _ = os.Remove(seenPath(home, role)) }

// Deliver is role's decisions for this turn, "" when its context already
// holds the same list from the last RepeatAfter, and the commit that records
// it as seen (call it once the text was printed).
func Deliver(home, role string, now time.Time) (string, func()) {
	noop := func() {}
	if role != "commander" && !strings.HasPrefix(role, "manager-") {
		return "", noop
	}
	all, err := Load(home)
	if err != nil {
		return "The user's standing decisions are unavailable (" + err.Error() + "): do not act on what you remember of them.", noop
	}
	ds := ForRole(all, role)
	sig := ""
	for _, d := range ds {
		sig += d.ID + "\n"
	}
	var s seen
	raw, readErr := os.ReadFile(seenPath(home, role))
	had := readErr == nil && json.Unmarshal(raw, &s) == nil
	if had && s.Sig == sig && now.Sub(time.Unix(s.At, 0)) < RepeatAfter {
		return "", noop
	}
	if !had && len(ds) == 0 {
		return "", noop // nothing to say, nothing said before
	}
	return Text(ds), func() {
		out, _ := json.Marshal(seen{Sig: sig, At: now.Unix()})
		if os.MkdirAll(filepath.Dir(seenPath(home, role)), 0o700) == nil {
			_ = writeAtomic(seenPath(home, role), out)
		}
	}
}
