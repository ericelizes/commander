package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/example-user/commander/internal/store"
)

const JurisdictionConsolidationPath = "/api/jurisdiction/consolidate"
const LegacyPlanningInventoryPath = "/api/jurisdiction/legacy"

func (s *Server) serveLegacyPlanningInventory(w http.ResponseWriter, r *http.Request) {
	st := s.jurisdictionStore(w)
	if st == nil {
		return
	}
	pid, e := strconv.ParseInt(r.URL.Query().Get("project_id"), 10, 64)
	if e != nil || pid <= 0 {
		writeJSON(w, 400, map[string]string{"error": "project_id required"})
		return
	}
	result, e := st.LegacyPlanningInventory(r.Context(), pid)
	if e != nil {
		writeJSON(w, 409, map[string]string{"error": e.Error()})
		return
	}
	writeJSON(w, 200, result)
}
func (s *Server) serveJurisdictionConsolidation(w http.ResponseWriter, r *http.Request) {
	st := s.jurisdictionStore(w)
	if st == nil {
		return
	}
	var req struct {
		store.JurisdictionConsolidation
		Env []string `json:"env,omitempty"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	d.DisallowUnknownFields()
	if e := d.Decode(&req); e != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid consolidation request"})
		return
	}
	if e := d.Decode(new(any)); e != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "one consolidation request required"})
		return
	}
	if len(req.Env) > 64 {
		writeJSON(w, 400, map[string]string{"error": "bounded attribution required"})
		return
	}
	for _, entry := range req.Env {
		key, value, _ := strings.Cut(entry, "=")
		if value != "" && (key == "COMMANDER_RUN_ID" || key == "COMMANDER_RUN_SESSION" || key == "COMMANDER_MANAGER_ROLE") {
			writeJSON(w, 403, map[string]string{"error": "execution attribution cannot consolidate planning ownership"})
			return
		}
	}
	result, e := st.ConsolidateLegacyPlanning(r.Context(), roleOf(req.Env), req.JurisdictionConsolidation)
	if e != nil {
		writeJSON(w, 409, map[string]string{"error": e.Error()})
		return
	}
	writeJSON(w, 200, result)
}
