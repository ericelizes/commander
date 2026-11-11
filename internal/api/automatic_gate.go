package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/gates"
)

type automaticGateFlight struct {
	done   chan struct{}
	result beads.AutomaticGateResponse
}
type automaticGateFlights struct {
	mu      sync.Mutex
	running map[string]*automaticGateFlight
	slots   chan struct{}
}

func (f *automaticGateFlights) do(ctx context.Context, key string, run func(chan struct{}) beads.AutomaticGateResponse) (result beads.AutomaticGateResponse) {
	f.mu.Lock()
	if f.running == nil {
		f.running = map[string]*automaticGateFlight{}
		f.slots = make(chan struct{}, 4)
	}
	if existing := f.running[key]; existing != nil {
		f.mu.Unlock()
		select {
		case <-existing.done:
			return existing.result
		case <-ctx.Done():
			return beads.AutomaticGateResponse{Exit: 1, Error: ctx.Err().Error()}
		}
	}
	entry := &automaticGateFlight{done: make(chan struct{})}
	f.running[key] = entry
	slots := f.slots
	f.mu.Unlock()
	result = beads.AutomaticGateResponse{Exit: 1, Error: "automatic gate check did not finish"}
	defer func() { f.mu.Lock(); entry.result = result; delete(f.running, key); close(entry.done); f.mu.Unlock() }()
	result = run(slots)
	return result
}

type automaticGateSnapshot struct {
	bead            beads.Bead
	detail          beads.Detail
	definition, dir string
	projectID       int64
}

func (s *Server) loadAutomaticGate(ctx context.Context, dir, id, actor string) (automaticGateSnapshot, error) {
	var out automaticGateSnapshot
	if s.opts.StateStore == nil || !s.opts.StateStore.IsPostgres() || !beads.PostgresConfigured() {
		return out, fmt.Errorf("automatic gate checks require the canonical PostgreSQL service")
	}
	if dir == "" || !filepath.IsAbs(dir) || id == "" || len(id) > 512 {
		return out, fmt.Errorf("automatic gate check requires an absolute project directory and bead")
	}
	if info, e := os.Stat(filepath.Join(dir, ".beads")); e != nil || !info.IsDir() {
		return out, fmt.Errorf("not a beads project")
	}
	real, e := filepath.EvalSymlinks(dir)
	if e != nil {
		return out, e
	}
	out.dir = real
	if e = s.opts.StateStore.DB().QueryRowContext(ctx, `SELECT id FROM projects WHERE path=? OR repo_path=? ORDER BY CASE WHEN path=? THEN 0 ELSE 1 END LIMIT 1`, real, real, real).Scan(&out.projectID); e != nil {
		return out, fmt.Errorf("automatic gate project is not registered: %w", e)
	}
	client := beads.New()
	b, e := client.Show(ctx, real, id)
	if e != nil {
		return out, e
	}
	if b == nil {
		return out, beads.ErrNotFound
	}
	if actor != "" && actor != b.PlanningCommander {
		return out, fmt.Errorf("automatic gate check belongs to planning Commander %s", b.PlanningCommander)
	}
	if !slices.Contains(b.Labels, "kind:gate") || !slices.Contains(b.Labels, "verifier:auto") {
		return out, fmt.Errorf("%s is not an automatic gate", id)
	}
	if slices.Contains(b.Labels, "retired") || slices.Contains(b.Labels, "superseded") {
		return out, fmt.Errorf("retired gate %s must be reconciled rather than re-executed", id)
	}
	for _, label := range b.Labels {
		if strings.HasPrefix(label, "workflow-template:") || strings.HasPrefix(label, "workflow-step:") {
			return out, fmt.Errorf("generated workflow steps use their workflow verifier rather than shell gate evaluation")
		}
	}
	if strings.TrimSpace(b.Description) == "" || len(b.Description) > 64<<10 {
		return out, fmt.Errorf("automatic gate needs a bounded nonempty check definition")
	}
	details, e := beads.FreshDetails(ctx, client, real, []string{id})
	if e != nil {
		return out, e
	}
	d, ok := details[id]
	if !ok {
		return out, fmt.Errorf("automatic gate has no fresh definition")
	}
	out.bead = *b
	out.detail = d
	out.definition = beads.CompletionDefinition(*b, d)
	return out, nil
}

// The command runs outside the graph write lock. The service then compares the
// exact definition and recorded versions under that lock before publishing an
// outcome. A caller cannot provide a successful result or replay another run.
func (s *Server) serveAutomaticGateCheck(w http.ResponseWriter, r *http.Request) {
	var req beads.AutomaticGateRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if e := decoder.Decode(&req); e != nil {
		writeJSON(w, http.StatusBadRequest, beads.AutomaticGateResponse{Exit: 2, Error: "invalid automatic gate request"})
		return
	}
	if e := decoder.Decode(new(any)); e != io.EOF {
		writeJSON(w, http.StatusBadRequest, beads.AutomaticGateResponse{Exit: 2, Error: "one automatic gate request required"})
		return
	}
	if len(req.Env) > 64 {
		writeJSON(w, http.StatusBadRequest, beads.AutomaticGateResponse{Exit: 2, Error: "too many attribution fields"})
		return
	}
	if beads.HasExecutionAttribution(req.Env) {
		writeJSON(w, http.StatusOK, beads.AutomaticGateResponse{Exit: 1, Error: "execution sessions cannot run planning gate checks"})
		return
	}
	actor := roleOf(req.Env)
	first, e := s.loadAutomaticGate(r.Context(), req.Dir, req.Bead, actor)
	if e != nil {
		writeJSON(w, http.StatusOK, beads.AutomaticGateResponse{Exit: 1, Error: e.Error()})
		return
	}
	key := fmt.Sprintf("%d/%s/%s/%d/%d", first.projectID, req.Bead, first.definition, first.bead.PlanRevision, first.bead.ProgressSeq)
	result := s.automaticGateChecks.do(r.Context(), key, func(slots chan struct{}) beads.AutomaticGateResponse {
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
		defer cancel()
		fail := func(e error) beads.AutomaticGateResponse {
			return beads.AutomaticGateResponse{Exit: 1, Error: e.Error(), Bead: req.Bead}
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		case <-ctx.Done():
			return fail(ctx.Err())
		}
		current, e := s.loadAutomaticGate(ctx, first.dir, req.Bead, actor)
		if e != nil {
			return fail(e)
		}
		if current.definition != first.definition || current.bead.PlanRevision != first.bead.PlanRevision || current.bead.ProgressSeq != first.bead.ProgressSeq {
			return fail(fmt.Errorf("automatic gate changed before its check could start"))
		}
		runner := s.opts.AutomaticGateRun
		if runner == nil {
			runner = gates.RunShell
		}
		status, evidence := gates.EvalAuto(ctx, runner, current.dir, current.bead.Description)
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		checkedAt := time.Now().Unix()
		receipt := beads.AutomaticGateResult{ProjectID: current.projectID, Bead: req.Bead, Definition: current.definition, PlanRevision: current.bead.PlanRevision, ProgressSeq: current.bead.ProgressSeq, Status: status, Evidence: evidence, CheckedAt: checkedAt}
		// The service records its own check under the logical planning identity.
		// Execution-attributed callers were rejected before evaluating the command.
		writeCtx := beads.WithDoorEnv(ctx, []string{"COMMANDER_ROLE=" + actor, "COMMANDER_RUN_ID=", "COMMANDER_RUN_SESSION="})
		writeCtx = beads.WithAutomaticGateResult(writeCtx, receipt)
		writeCtx = beads.WithCompletionValidator(writeCtx, s.completionValidator(current.dir, actor, nil))
		response := beads.ApplyOps(writeCtx, current.dir, beads.CanonicalAutomaticGateOperations(receipt), true, true, nil, nil)
		if response.Exit != 0 {
			return fail(fmt.Errorf("automatic gate result not recorded: %s", response.Error))
		}
		if s.opts.Source != nil && !beads.AutomaticGateUnchanged(writeCtx) {
			go s.refreshAfter(time.Now())
		}
		return beads.AutomaticGateResponse{Bead: req.Bead, Status: status, Evidence: evidence, CheckedAt: checkedAt, Unchanged: beads.AutomaticGateUnchanged(writeCtx)}
	})
	writeJSON(w, http.StatusOK, result)
}
