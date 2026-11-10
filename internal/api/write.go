package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/example-user/commander/internal/beads"
)

// settleWait bounds how long a write waits for the daemon's state to show it.
var settleWait = 20 * time.Second

// defaultRefreshBudget bounds the snapshot load a settle starts, however long its caller
// waits. It starts once the load holds refreshMu: time queued behind another load
// is not the load's, and a load run on an expired context reads as an empty board.
const defaultRefreshBudget = 3 * time.Minute

// writeDeadline bounds all the operations of one request together (each br call
// also has its own 40s limit); past it the remaining operations do not start.
var writeDeadline = 2 * time.Minute

// WriteRunner runs one br write (argv without the binary) in dir; Options.Write
// overrides beads.RunBr so tests need no br.
type WriteRunner func(ctx context.Context, dir string, args []string) (stdout, stderr []byte, err error)

// BatchRunner runs a bd batch script in dir; Options.Batch overrides beads.RunBatch.
type BatchRunner = beads.BatchRunner

func defaultWriteRunner(ctx context.Context, dir string, args []string) ([]byte, []byte, error) {
	return beads.RunBr(ctx, dir, args...)
}

// serveWrite is the write door: POST /api/write {dir, args | ops, atomic} runs
// the write at once (writes are concurrent, bounded only by the limiter caps;
// several ops of one request are one bd transaction where bd has one, see
// beads.ApplyOps), then returns once a snapshot built after it is the daemon's
// current one.
func (s *Server) serveWrite(w http.ResponseWriter, r *http.Request) {
	var req beads.DoorRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, beads.DoorResponse{Exit: 2, Error: "bad request: " + err.Error()})
		return
	}
	ops := req.Ops
	if len(ops) == 0 {
		ops = [][]string{req.Args}
	}
	if !beads.OpsWritable(ops) {
		writeJSON(w, http.StatusBadRequest, beads.DoorResponse{Exit: 2, Error: fmt.Sprintf("not a br write the door accepts (one to %d write operations)", beads.MaxOps)})
		return
	}
	if fi, err := os.Stat(filepath.Join(req.Dir, ".beads")); req.Dir == "" || !filepath.IsAbs(req.Dir) || err != nil || !fi.IsDir() {
		writeJSON(w, http.StatusBadRequest, beads.DoorResponse{Exit: 2, Error: "dir is not a beads project"})
		return
	}
	if role := roleOf(req.Env); role != "" {
		if err := s.checkWrite(r.Context(), req.Dir, role, ops); err != nil {
			logDoorRefusal(req.Dir, role, ops, err)
			writeJSON(w, http.StatusOK, beads.DoorResponse{Exit: 1, Error: err.Error() + " (refused at the write door; nothing was written)", Settled: true})
			return
		}
	}
	var plan *intentPlan
	if req.Intent != nil {
		var err error
		ops, plan, err = s.planIntent(r.Context(), req.Dir, roleOf(req.Env), *req.Intent, ops)
		if err != nil {
			logDoorRefusal(req.Dir, roleOf(req.Env), ops, err)
			writeJSON(w, http.StatusOK, beads.DoorResponse{Exit: 1, Error: err.Error() + " (refused at the write door; nothing was written)", Settled: true})
			return
		}
	}
	run := s.opts.Write
	if run == nil {
		run = defaultWriteRunner
	}
	batch, batchOK := s.opts.Batch, true
	if batch == nil {
		batch, batchOK = beads.RunBatch, beads.BatchBackend()
	}
	// A caller that hangs up must not cancel a write already running.
	ctx, cancel := context.WithTimeout(beads.WithDoorEnv(context.WithoutCancel(r.Context()), req.Env), writeDeadline)
	defer cancel()
	backendStart := time.Now()
	resp := beads.ApplyOps(ctx, req.Dir, ops, req.Atomic, batchOK, run, batch)
	if resp.Exit == 0 && plan != nil {
		resp.Holders = plan.holders
		s.patchNow(plan.after) // the board shows the write at once, pending until a load reads it (I8)
	}
	resp.BackendMS = float64(time.Since(backendStart)) / float64(time.Millisecond)
	projectionStart := time.Now()
	if req.NoWait && s.opts.Source != nil {
		go s.refreshAfter(projectionStart)
		resp.ProjectionPending = true
	} else {
		resp.Settled = s.settle(context.WithoutCancel(ctx), projectionStart)
		resp.ProjectionPending = !resp.Settled
	}
	resp.ProjectionMS = float64(time.Since(projectionStart)) / float64(time.Millisecond)
	writeJSON(w, http.StatusOK, resp)
}

// settleReloads and settleReloadAfter bound the extra loads a settle makes after one that kept the last rows.
const (
	settleReloads     = 3
	settleReloadAfter = 250 * time.Millisecond
)

// settle returns once a snapshot loaded after `after` is current. Writers that
// finish while a load is running share the next one. Only the wait is bounded by
// settleWait: the load itself keeps running (bounded by refreshBudget, on a fresh
// context so the caller's door env never reaches the shared read) so a slow
// settle never cancels a br read half way. A load that outruns refreshBudget
// publishes nothing: the last board stays and this answers false. A load with its
// context alive whose reads failed is still published, marked degraded.
func (s *Server) settle(ctx context.Context, after time.Time) bool {
	if s.opts.Source == nil {
		return true
	}
	done := make(chan struct{})
	s.settling.Go(func() {
		defer close(done)
		s.refreshAfter(after)
		// A load that overlapped other writers kept the last rows, which predate this write (I8:
		// the board is never older than a write it answered). Load again, a few times, while the
		// wait lasts: the overlap is usually over by then.
		for i := 0; i < settleReloads && s.loadKept.Load(); i++ {
			select {
			case <-time.After(settleReloadAfter):
			case <-ctx.Done():
				return
			}
			s.refreshMu.Lock()
			rctx, cancel := context.WithTimeout(context.Background(), s.refreshBudget)
			s.refreshLocked(rctx)
			cancel()
			s.refreshMu.Unlock()
		}
	})
	wait := time.NewTimer(settleWait)
	defer wait.Stop()
	select {
	case <-done:
		return s.lastLoadClean()
	case <-wait.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// refreshAfter refreshes unless a load that began after `after` already
// finished while this call waited for the lock. Calls queued behind a slow load
// therefore drain without loading again (no pile-up of loads).
func (s *Server) refreshAfter(after time.Time) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if s.loadStarted.After(after) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.refreshBudget)
	defer cancel()
	s.refreshLocked(beads.WithReadCaller(ctx, "api-settle"))
}

// lastLoadClean is false when the latest load was degraded, kept last-good rows
// or ended with its context: the write is done, but the snapshot may not show it yet.
func (s *Server) lastLoadClean() bool { return !s.loadKept.Load() }
