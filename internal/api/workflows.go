package api

import (
	"net/http"

	"github.com/example-user/commander/internal/workflowtemplate"
)

// serveWorkflows is the workflow catalog: every template with what it applies
// to, when to use and skip it, and its steps, as the planners read them.
func (s *Server) serveWorkflows(w http.ResponseWriter, r *http.Request) {
	all, err := workflowtemplate.Catalog()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflows": all})
}
