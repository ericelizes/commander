package api

import (
	"errors"
	"github.com/example-user/commander/internal/store"
	"net/http"
	"strconv"
	"time"

	"github.com/example-user/commander/internal/livedata"
)

func (s *Server) serveStateEvents(w http.ResponseWriter, r *http.Request) {
	if s.opts.StateStore == nil {
		http.Error(w, "durable state events unavailable", http.StatusServiceUnavailable)
		return
	}
	cursor, e := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	if r.URL.Query().Get("since") == "" {
		cursor = 0
		e = nil
	}
	if e != nil || cursor < 0 {
		http.Error(w, "invalid event cursor", http.StatusBadRequest)
		return
	}
	generation, _, e := s.opts.StateStore.StateGeneration(r.Context())
	if e != nil {
		http.Error(w, "current database state unavailable", http.StatusServiceUnavailable)
		return
	}
	if cursor > 0 && r.URL.Query().Get("generation") != generation {
		writeJSON(w, http.StatusConflict, map[string]any{"resync": true, "generation": generation})
		return
	}
	events, e := s.opts.StateStore.StateEventsSince(r.Context(), cursor, 1000)
	if errors.Is(e, store.ErrReplayExpired) {
		writeJSON(w, http.StatusConflict, map[string]any{"resync": true, "generation": generation})
		return
	}
	if e != nil {
		http.Error(w, "current database state unavailable", http.StatusServiceUnavailable)
		return
	}
	if len(events) > 0 {
		cursor = events[len(events)-1].Seq
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "cursor": cursor, "generation": generation})
}

func unavailablePostgresSnapshot() livedata.Snapshot {
	return livedata.Snapshot{Storage: "postgres", Executions: []livedata.SnapExecution{}, GeneratedAt: time.Now(), Projects: []livedata.SnapProject{}, Workstreams: []livedata.SnapWorkstream{}, NeedsYou: []livedata.NeedsYou{}, Agents: map[string]livedata.SnapAgent{}, Unread: map[string]int{}, Watch: map[string]livedata.SnapWatch{}, Degraded: &livedata.SnapDegraded{Reason: "Current database state is unavailable; previous rows are not shown"}}
}
