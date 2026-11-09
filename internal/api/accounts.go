package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"runtime"
	"strings"

	"github.com/example-user/commander/internal/claudeacct"
)

// serveAccountAdd creates a Claude account ({"name": ...}) and opens a
// terminal running its login command. The user logs in there; the app never
// sees a credential, and the account list shows it logged in once it is.
func (s *Server) serveAccountAdd(w http.ResponseWriter, r *http.Request) {
	e := s.chatEngine(w, r)
	if e == nil {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad JSON body: " + err.Error()})
		return
	}
	a, err := claudeacct.Create(e.Home(), strings.TrimSpace(req.Name))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	s.openLogin(w, r, e.Home(), a.Name, "")
}

// serveAccountLogin opens the login terminal of an existing account again.
func (s *Server) serveAccountLogin(w http.ResponseWriter, r *http.Request) {
	e := s.chatEngine(w, r)
	if e == nil {
		return
	}
	name := r.PathValue("name")
	if !claudeacct.Exists(e.Home(), name) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no Claude account " + name})
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad JSON body"})
			return
		}
	}
	if !claudeacct.ValidLoginEmail(req.Email) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid email"})
		return
	}
	s.openLogin(w, r, e.Home(), name, req.Email)
}

func (s *Server) openLogin(w http.ResponseWriter, r *http.Request, home, name, email string) {
	before := claudeacct.Get(home, name)
	cmd := claudeacct.LoginCommand(home, name, email)
	open := s.opts.OpenTerminal
	if open == nil {
		open = OpenLoginTerminal
	}
	if err := open(r.Context(), cmd); err != nil {
		// The account exists; the user can still run the line themselves.
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account": name, "login_command": cmd, "before": before, "opened": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account": name, "login_command": cmd, "before": before, "opened": true})
}

// OpenLoginTerminal opens Terminal.app running command (macOS only).
func OpenLoginTerminal(ctx context.Context, command string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("opening a terminal is macOS only: run the login command yourself")
	}
	quoted := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(command) + `"`
	return exec.CommandContext(ctx, "osascript", "-e", `tell application "Terminal"`, "-e", "activate",
		"-e", "do script "+quoted, "-e", "end tell").Run()
}
