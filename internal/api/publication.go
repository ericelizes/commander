package api

import (
	"context"
	"encoding/json"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/gates"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func (s *Server) servePublication(w http.ResponseWriter, r *http.Request) {
	var req beads.PublicationRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	decoder.DisallowUnknownFields()
	if e := decoder.Decode(&req); e != nil {
		writeJSON(w, http.StatusBadRequest, beads.DoorResponse{Exit: 2, Error: "invalid publication request"})
		return
	}
	if e := decoder.Decode(new(any)); e != io.EOF {
		writeJSON(w, http.StatusBadRequest, beads.DoorResponse{Exit: 2, Error: "one publication request required"})
		return
	}
	if len(req.Env) > 64 || !filepath.IsAbs(req.Dir) {
		writeJSON(w, http.StatusBadRequest, beads.DoorResponse{Exit: 2, Error: "publication requires an absolute project directory and bounded attribution"})
		return
	}
	if beads.HasExecutionAttribution(req.Env) {
		writeJSON(w, http.StatusOK, beads.DoorResponse{Exit: 1, Error: "execution sessions cannot publish planning gates"})
		return
	}
	actor := roleOf(req.Env)

	if info, e := os.Stat(filepath.Join(req.Dir, ".beads")); e != nil || !info.IsDir() {
		writeJSON(w, http.StatusBadRequest, beads.DoorResponse{Exit: 2, Error: "publication requires a registered beads project"})
		return
	}
	real, e := filepath.EvalSymlinks(req.Dir)
	if e != nil {
		writeJSON(w, http.StatusBadRequest, beads.DoorResponse{Exit: 2, Error: e.Error()})
		return
	}
	req.Dir = real
	requestCtx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	ctx := beads.WithDoorEnv(requestCtx, []string{"COMMANDER_ROLE=" + actor, "COMMANDER_RUN_ID=", "COMMANDER_RUN_SESSION="})
	result := beads.ApplyPublication(ctx, s.opts.StateStore, req, func(ctx context.Context, client beads.Client) error {
		if len(req.Waiters) == 0 {
			return gates.PublishPlan(ctx, client, req.Dir, req.Gate, req.Parent, req.Dry || req.Propose)
		}
		return gates.RepairPublication(ctx, client, req.Dir, req.Gate, req.Parent, req.Waiters, req.Reason, actor, req.Dry || req.Propose)
	})
	if result.Exit == 0 && !result.Replayed && !req.Dry && !req.Propose && s.opts.Source != nil {
		go s.refreshAfter(time.Now())
	}
	writeJSON(w, http.StatusOK, result)
}
