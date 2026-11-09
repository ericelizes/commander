package boardturn

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"
)

// A delivery is the whole board when the agent has not seen one in this
// context, every FullEvery deliveries or FullAfter, or when more than a third
// of the lines changed; otherwise only what changed (~1.5k tokens vs a few lines).
const (
	FullEvery = 10
	FullAfter = 30 * time.Minute
)

// seen is what role was last shown: the version and each line as delivered.
type seen struct {
	Version int         `json:"version"`
	Entries []seenEntry `json:"entries"`
	Turns   int         `json:"turns"` // deliveries since the last full board
	FullAt  int64       `json:"full_at"`
}

type seenEntry struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Line string `json:"line"`
}

func loadSeen(home, role string) (seen, bool) {
	var s seen
	raw, err := os.ReadFile(seenPath(home, role))
	if err != nil || json.Unmarshal(raw, &s) != nil {
		return seen{}, false
	}
	return s, true
}

// Deliver is role's board for this turn and the commit that records it as
// seen; call commit only once the text was printed.
func Deliver(home, role string, now time.Time) (string, func()) {
	noop := func() {}
	if !Role(role) {
		return "", noop
	}
	f, err := Read(home, role, now)
	if err != nil {
		return fmt.Sprintf("%s is unavailable: cmdrd is not updating it (%v). Do not describe the board from memory: say it is unavailable.", whose(role), err), noop
	}
	s, ok := loadSeen(home, role)
	changes := changed(s, f.Entries)
	full := !ok || s.Turns+1 >= FullEvery || now.Sub(time.Unix(s.FullAt, 0)) >= FullAfter || len(changes)*3 > len(f.Entries)
	next := seen{Version: f.Version, Turns: s.Turns + 1, FullAt: s.FullAt}
	for _, e := range f.Entries {
		next.Entries = append(next.Entries, seenEntry{ID: e.ID, Key: e.Key, Line: e.Line()})
	}
	var text string
	switch {
	case full:
		next.Turns, next.FullAt = 0, now.Unix()
		text = Full(f, role)
	case len(changes) == 0:
		text = fmt.Sprintf("%s v%d: unchanged since you last saw it.", whose(role), f.Version)
	default:
		text = fmt.Sprintf("%s v%d (since v%d, which you saw; every other line still reads as you saw it):\n%s",
			whose(role), f.Version, s.Version, strings.Join(changes, "\n"))
	}
	return text, func() {
		raw, _ := json.Marshal(next)
		_ = writeAtomic(seenPath(home, role), raw)
	}
}

// Full is the whole board with its intro.
func Full(f File, role string) string {
	lines := body(f.Entries)
	if len(lines) == 0 {
		lines = []string{"(no workstreams)"}
	}
	return fmt.Sprintf("%s v%d at %s, exactly as the user's sidebar shows it (added automatically; the user did not type this). "+
		"Rows are #N, nested workstreams #N.M, NEEDS YOU items ask #N:\n%s",
		whose(role), f.Version, time.Unix(f.At, 0).Format("15:04"), strings.Join(lines, "\n"))
}

func whose(role string) string {
	if role == "commander" {
		return "The user's board"
	}
	return "Your workstream on the user's board"
}

func body(es []Entry) []string {
	var out []string
	asks := 0
	for _, e := range es {
		if e.Ask {
			asks++
		}
	}
	headed := false
	for _, e := range es {
		switch {
		case e.Ask:
			if !headed {
				out, headed = append(out, fmt.Sprintf("Needs you (%d):", asks)), true
			}
			out = append(out, "  "+e.Line())
		case e.Track:
			out = append(out, "  "+e.Line())
		default:
			out = append(out, e.Line())
		}
	}
	return out
}

// changed is the diff from what s showed to es: "~" a line that reads
// differently, "+" a new line, "-" one that is gone (as it last read).
func changed(s seen, es []Entry) []string {
	before := make(map[string]seenEntry, len(s.Entries))
	for _, e := range s.Entries {
		before[e.ID] = e
	}
	now := make(map[string]bool, len(es))
	var out []string
	for _, e := range es {
		now[e.ID] = true
		if b, ok := before[e.ID]; !ok {
			out = append(out, "+ "+e.Line())
		} else if b.Key != e.Key {
			out = append(out, "~ "+e.Line())
		}
	}
	for _, e := range s.Entries {
		if !now[e.ID] {
			out = append(out, "- "+e.Line)
		}
	}
	return out
}

// AfterWrite is the board lines a state write touched, read right after it
// (cmdrd settles the board before the write returns), so the agent sees what
// the row says now in the same turn. "" when no line names a bead in command.
func AfterWrite(home, role, command string, now time.Time) string {
	f, err := Read(home, role, now)
	if err != nil {
		return ""
	}
	tokens := strings.FieldsFunc(command, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '.' && r != '-' && r != '_'
	})
	var hit []string
	for _, e := range f.Entries {
		if e.BeadID == "" {
			continue
		}
		for _, t := range tokens {
			if t == e.BeadID || strings.HasPrefix(t, e.BeadID+".") {
				hit = append(hit, e.Line())
				break
			}
		}
	}
	if len(hit) == 0 {
		return ""
	}
	return fmt.Sprintf("After that write, the user's board v%d reads:\n%s", f.Version, strings.Join(hit, "\n"))
}
