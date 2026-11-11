package livedata

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/example-user/commander/internal/store"
)

// Wire is Data as JSON: the cockpit's own columns fetch it from cmdrd instead
// of each running br and reading state.db themselves (one reader, not nine).
type Wire struct {
	Data
	Ranks     []WireRank `json:"ranks"`
	BeadRanks []WireRank `json:"bead_ranks,omitempty"`
	LoadErr   string     `json:"load_err,omitempty"`
}

// WireRank is one workstream_ranks entry (a struct key cannot be a JSON map key).
type WireRank struct {
	ProjectID int64  `json:"project_id"`
	BeadID    string `json:"bead_id"`
	Rank      int    `json:"rank"`
}

// ToWire flattens d for JSON.
func ToWire(d Data) Wire {
	w := Wire{Data: d}
	w.Data.Ranks, w.Data.LoadErr = nil, nil
	for k, r := range d.Ranks {
		w.Ranks = append(w.Ranks, WireRank{ProjectID: k.ProjectID, BeadID: k.BeadID, Rank: r})
	}
	for k, r := range d.BeadRanks {
		w.BeadRanks = append(w.BeadRanks, WireRank{ProjectID: k.ProjectID, BeadID: k.BeadID, Rank: r})
	}
	if d.LoadErr != nil {
		w.LoadErr = d.LoadErr.Error()
	}
	return w
}

// FromWire rebuilds Data from its JSON form.
func FromWire(w Wire) Data {
	d := w.Data
	d.Ranks = make(map[store.WorkstreamKey]int, len(w.Ranks))
	for _, r := range w.Ranks {
		d.Ranks[store.WorkstreamKey{ProjectID: r.ProjectID, BeadID: r.BeadID}] = r.Rank
	}
	d.BeadRanks = make(map[store.WorkstreamKey]int, len(w.BeadRanks))
	for _, r := range w.BeadRanks {
		d.BeadRanks[store.WorkstreamKey{ProjectID: r.ProjectID, BeadID: r.BeadID}] = r.Rank
	}
	if w.LoadErr != "" {
		d.LoadErr = errors.New(w.LoadErr)
	}
	return d
}

// lastData holds the most recent Data a Source loaded, for /api/livedata.
type lastData struct {
	mu   sync.RWMutex
	data Data
	ok   bool
	err  error     // the latest load's error, even when data above is older
	at   time.Time // when a load last finished, failed or not; a spent one does not count
}

func (l *lastData) set(d Data) { l.mu.Lock(); l.data, l.ok = d, true; l.mu.Unlock() }

func (l *lastData) finished(err error) {
	l.mu.Lock()
	l.err, l.at = err, time.Now()
	l.mu.Unlock()
}

// noLoadFor is how long with no load finishing reads as beads hanging. A
// load dropped for its spent context raises nothing, and a count of them in a row
// would not catch this: only a settle's load has a deadline, so a hung read parks
// the poll's load on the lock those wait for, and any slow load the poll does
// finish would reset the count. Well above one settle's 3-minute budget. A hang in
// state.db raises nothing, as the watch that reads this parks on its own store read
// first, and neither does a wedged tmux: the minute loop lists panes with no timeout
// before it runs the watch. The gate watcher skips its poll while this is set (a
// load hung in beads or in the load's own tmux scrape); a state.db hang parks it on
// its own ListProjects read instead, and with only the ops socket wedged loads keep
// finishing and it keeps polling.
const noLoadFor = 10 * time.Minute

// LastLoadErr is the error of the most recent load (nil when it was clean),
// which Last hides by keeping the previous good data, or an error of its own
// once a load has finished before but none has for noLoadFor.
func (s *Source) LastLoadErr() error {
	s.last.mu.RLock()
	defer s.last.mu.RUnlock()
	if s.last.err == nil && !s.last.at.IsZero() && time.Since(s.last.at) > noLoadFor {
		return fmt.Errorf("beads not answering: no load has finished for %s", time.Since(s.last.at).Round(time.Minute))
	}
	return s.last.err
}

// Last returns the Data the most recent Snapshot call loaded.
//
// Ranks are re-read from state.db on every call: a load takes as long as its br
// reads (tens of seconds under load) and read its ranks first, so a move written
// meanwhile would otherwise not show until the next load finished.
func (s *Source) Last() (Data, bool) {
	if s.Deps.Store != nil && s.Deps.Store.IsPostgres() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		data, _ := s.Current(ctx)
		return data, true
	}

	s.last.mu.RLock()
	d, ok := s.last.data, s.last.ok
	s.last.mu.RUnlock()
	if ok {
		s.freshRanks(&d)
	}
	return d, ok
}

// freshRanks replaces d.Ranks with the stored ones (kept as loaded on error).
func (s *Source) freshRanks(d *Data) {
	if s.Deps.Store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if ranks, err := s.Deps.Store.ListWorkstreamRanks(ctx); err == nil {
		d.Ranks = ranks
	}
	if ranks, err := s.Deps.Store.ListBeadRanks(ctx); err == nil {
		d.BeadRanks = ranks
	}
}
