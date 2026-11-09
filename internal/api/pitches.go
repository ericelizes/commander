package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/example-user/commander/internal/store"
)

// PitchStore is the part of the store /api/pitches reads.
type PitchStore interface {
	GetProjectByName(ctx context.Context, name string) (*store.Project, error)
	ListPitches(ctx context.Context, projectID int64, under string) ([]store.Pitch, error)
}

// servePitches lists every version of the pitches on a bead and the beads under
// it: ?project=<name>&bead=<workstream, track or step id>.
func (s *Server) servePitches(w http.ResponseWriter, r *http.Request) {
	if s.opts.Pitches == nil {
		http.Error(w, "pitches unavailable", http.StatusServiceUnavailable)
		return
	}
	p, err := s.opts.Pitches.GetProjectByName(r.Context(), r.URL.Query().Get("project"))
	if err != nil || p == nil {
		http.Error(w, "unknown project", http.StatusNotFound)
		return
	}
	list, err := s.opts.Pitches.ListPitches(r.Context(), p.ID, r.URL.Query().Get("bead"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []store.Pitch{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"pitches": list})
}
