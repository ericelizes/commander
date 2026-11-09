package chat

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// eventLog is the append-only per-role log at <home>/chat/<role>.jsonl. It
// holds every persisted event (see persisted) so the app can replay a chat
// after cmdrd or the app restarts.
type eventLog struct {
	path string
	f    *os.File
}

func openEventLog(dir, role string) (*eventLog, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("chat: mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, role+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("chat: open log: %w", err)
	}
	return &eventLog{path: path, f: f}, nil
}

func (l *eventLog) append(e Event) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = l.f.Write(append(raw, '\n'))
	return err
}

func (l *eventLog) close() error { return l.f.Close() }

// readLog returns the events in path with since < seq < before (before 0 =
// no upper bound). A torn last line (crash mid-write) is skipped.
func readLog(path string, since, before uint64) ([]Event, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if e.Seq <= since || (before > 0 && e.Seq >= before) {
			continue
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// logSummary is what startup needs from an existing log.
type logSummary struct {
	lastSeq  uint64
	sessions map[string]bool // session ids that have events in the log
	// native marks sessions with an event the engine itself produced (live or
	// engine source): their transcript is already covered by live events.
	native map[string]bool
	// hydrated counts the transcript events published per session.
	hydrated map[string]int
}

func summarizeLog(path string) (logSummary, error) {
	s := logSummary{sessions: map[string]bool{}, native: map[string]bool{}, hydrated: map[string]int{}}
	events, err := readLog(path, 0, 0)
	for _, e := range events {
		if e.Seq > s.lastSeq {
			s.lastSeq = e.Seq
		}
		if e.SessionID != "" {
			s.sessions[e.SessionID] = true
			if e.Source == SourceTranscript {
				s.hydrated[e.SessionID]++
			} else {
				s.native[e.SessionID] = true
			}
		}
	}
	return s, err
}

// meta is the small per-role state kept beside the log.
type meta struct {
	SessionID    string  `json:"session_id,omitempty"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	CostKnown    bool    `json:"cost_known"`
	CLIVersion   string  `json:"cli_version,omitempty"`
	// Running is true while the engine wants the process up: an explicit Stop
	// clears it; a cmdrd shutdown keeps it so the next cmdrd resumes the chat.
	Running bool `json:"running"`
}

func metaPath(dir, role string) string { return filepath.Join(dir, role+".meta.json") }

func readMeta(dir, role string) meta {
	var m meta
	if raw, err := os.ReadFile(metaPath(dir, role)); err == nil {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}

func writeMeta(dir, role string, m meta) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := metaPath(dir, role) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, metaPath(dir, role))
}
