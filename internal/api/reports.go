package api

import (
	"net/http"
	"path/filepath"

	"github.com/example-user/commander/internal/reports"
)

// serveReport serves one report file that `cmdr tab add --file` imported.
func (s *Server) serveReport(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !reports.ValidName(name) {
		http.NotFound(w, r)
		return
	}
	home := filepath.Dir(s.opts.ConfigPath)
	if s.opts.Chat != nil {
		home = s.opts.Chat.Home()
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, filepath.Join(reports.Dir(home), name))
}
