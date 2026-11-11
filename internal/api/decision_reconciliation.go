package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/example-user/commander/internal/store"
)

const DecisionReconciliationPath = "/api/decision/reconcile"

func (s *Server) serveDecisionReconciliation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		store.DecisionReconciliation
		Env []string `json:"env,omitempty"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	d.DisallowUnknownFields()
	if e := d.Decode(&req); e != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid decision reconciliation"})
		return
	}
	if e := d.Decode(new(any)); e != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "one reconciliation required"})
		return
	}
	if len(req.Env) > 64 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bounded attribution required"})
		return
	}
	for _, entry := range req.Env {
		key, value, _ := strings.Cut(entry, "=")
		if value != "" && (key == "COMMANDER_RUN_ID" || key == "COMMANDER_RUN_SESSION" || key == "COMMANDER_MANAGER_ROLE") {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "execution attribution cannot reconcile a human decision"})
			return
		}
	}
	if s.opts.StateStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "canonical state unavailable"})
		return
	}
	result, e := s.opts.StateStore.ReconcileDecision(r.Context(), roleOf(req.Env), req.DecisionReconciliation)
	if e != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": e.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}
