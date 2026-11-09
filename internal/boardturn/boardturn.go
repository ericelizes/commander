// Package boardturn carries the user's board into every agent turn. cmdrd
// writes each role's board to ~/.commander/board/<role>.json (and a .txt copy)
// whenever it changes; the mail hook reads it on every turn and hands the
// agent the whole board or what changed since the version it last saw.
package boardturn

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Entry is one board line: a workstream row (ID "3"), a nested one (ID "3.2") or a
// NEEDS YOU item (ID "ask:<key>").
type Entry struct {
	ID     string `json:"id"`
	Label  string `json:"label"` // "#3", "#3.2", "ask #1.3"
	Kind   string `json:"kind"`
	Text   string `json:"text"` // the line after its label, ages included
	Key    string `json:"key"`  // Text without ages: what versions and diffs compare
	BeadID string `json:"bead_id,omitempty"`
	Track  bool   `json:"track,omitempty"`
	Ask    bool   `json:"ask,omitempty"`
}

// Line is the entry as the agent reads it.
func (e Entry) Line() string { return e.Label + " " + e.Text }

// broken is the line of a row that is not fine whatever anyone says: the
// stalled kinds (Stalled, State missing, Agent stopped ...) and stuck agents.
func (e Entry) broken() bool { return e.Kind == "stalled" || e.Kind == "stuck" }

// File is one role's board as cmdrd last wrote it.
type File struct {
	Version int     `json:"version"`
	At      int64   `json:"at"` // unix s
	Entries []Entry `json:"entries"`
}

// MaxAge is how old a board file may be before the hook calls the board
// unavailable: cmdrd rewrites every file at least every Heartbeat.
const (
	Heartbeat = 30 * time.Second
	MaxAge    = 90 * time.Second
)

var roleName = regexp.MustCompile(`^(commander|manager-[A-Za-z0-9][A-Za-z0-9._-]*)$`)

// Role reports whether role gets a board: Commander and the Managers.
func Role(role string) bool { return roleName.MatchString(role) && !strings.Contains(role, "..") }

// Dir is where the board files live.
func Dir(home string) string { return filepath.Join(home, "board") }

func filePath(home, role string) string { return filepath.Join(Dir(home), role+".json") }
func seenPath(home, role string) string { return filepath.Join(Dir(home), "seen", role+".json") }

// Read is role's board, or an error when cmdrd has not written it within MaxAge.
func Read(home, role string, now time.Time) (File, error) {
	var f File
	raw, err := os.ReadFile(filePath(home, role))
	if err != nil {
		return f, errors.New("no board file")
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, errors.New("unreadable board file")
	}
	if age := now.Sub(time.Unix(f.At, 0)); age > MaxAge {
		return f, fmt.Errorf("written %s ago", age.Round(time.Second))
	}
	return f, nil
}

// MarkFull makes role's next delivery the whole board: its context no longer
// holds the one it saw (a new session, a resume, a compaction).
func MarkFull(home, role string) {
	if Role(role) {
		_ = os.Remove(seenPath(home, role))
	}
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// signature is what a version stands for: the lines without their ages.
func signature(es []Entry) string {
	var b strings.Builder
	for _, e := range es {
		b.WriteString(e.ID + "\x00" + e.Label + "\x00" + e.Key + "\n")
	}
	return b.String()
}
