package api

import (
	"encoding/json"
	"github.com/example-user/commander/internal/agentrt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/example-user/commander/internal/config"
)

// Settings is what the app's Settings modal reads.
type Settings struct {
	LinearWorkspace    string  `json:"linear_workspace"`
	LinearTeam         string  `json:"linear_team"`
	UserName           string  `json:"user_name"`
	Timezone           string  `json:"timezone,omitempty"` // [user] timezone (IANA), "" = the browser zone
	Host               string  `json:"host"`
	CodexManagerShare  float64 `json:"codex_manager_share"`
	CodexOperatorShare float64 `json:"codex_operator_share"`
}

func (s *Server) settingsRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/settings", s.auth(http.HandlerFunc(s.serveSettings)))
	mux.Handle("POST /api/settings/runtime", s.auth(http.HandlerFunc(s.serveSettingsRuntime)))
	mux.Handle("POST /api/runtime/rebalance", s.auth(http.HandlerFunc(s.serveRebalance)))
}

func (s *Server) readSettings() (Settings, error) {
	cfg, err := config.Load(s.opts.ConfigPath)
	if err != nil {
		return Settings{Host: hostName(), UserName: config.UserName(nil)}, err
	}
	// What the config file says, else the default an unset key means.
	share := func(v *float64, def float64) float64 {
		if v != nil {
			return *v
		}
		return def
	}
	st := Settings{UserName: config.UserName(cfg), Host: hostName(), Timezone: strings.TrimSpace(cfg.User.Timezone),
		CodexManagerShare:  share(cfg.Runtime.CodexManagerShare, agentrt.DefaultCodexManagerShare),
		CodexOperatorShare: share(cfg.Runtime.CodexOperatorShare, agentrt.DefaultCodexOperatorShare)}
	local, err := config.LoadLocal(s.opts.ConfigPath)
	if err != nil {
		return st, err
	}
	st.LinearWorkspace = local.Integrations.Linear.Workspace
	if t := local.Integrations.Linear.Team; t != nil {
		st.LinearTeam = strings.TrimSpace(*t)
	}
	return st, nil
}

func (s *Server) serveSettings(w http.ResponseWriter, r *http.Request) {
	if s.opts.ConfigPath == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "no config path"})
		return
	}
	st, err := s.readSettings()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "settings": st})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "settings": st})
}

// serveSettingsRuntime writes [runtime] codex_manager_share and/or
// codex_operator_share (each 0-1; a field left out is left alone).
func (s *Server) serveSettingsRuntime(w http.ResponseWriter, r *http.Request) {
	if s.opts.ConfigPath == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "no config path"})
		return
	}
	var req struct {
		Manager  *float64 `json:"codex_manager_share"`
		Operator *float64 `json:"codex_operator_share"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad JSON body: " + err.Error()})
		return
	}
	for key, v := range map[string]*float64{"codex_manager_share": req.Manager, "codex_operator_share": req.Operator} {
		if v == nil {
			continue
		}
		if err := config.SetValue(s.opts.ConfigPath, "runtime", key, strconv.FormatFloat(*v, 'f', -1, 64)); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
	}
	st, err := s.readSettings()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "settings": st})
}

// serveRebalance runs `cmdr runtime rebalance` ({"dry_run": true} adds
// --dry-run) and returns its output.
func (s *Server) serveRebalance(w http.ResponseWriter, r *http.Request) {
	if s.opts.Run == nil {
		writeJSON(w, http.StatusServiceUnavailable, CmdResult{Exit: -1, Error: "no command runner"})
		return
	}
	var req struct {
		DryRun bool `json:"dry_run"`
	}
	if r.ContentLength != 0 {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req)
	}
	argv := []string{"runtime", "rebalance"}
	if req.DryRun {
		argv = append(argv, "--dry-run")
	}
	res := s.opts.Run(r.Context(), argv)
	code := http.StatusOK
	if !res.OK {
		code = http.StatusBadGateway
	}
	writeJSON(w, code, res)
}

// hostName is the machine cmdrd runs on, so an app on another machine can say
// it is connected remotely.
func hostName() string {
	h, _ := os.Hostname()
	return h
}
