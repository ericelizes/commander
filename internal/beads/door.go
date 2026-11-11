package beads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The write door: every br write funnels through cmdrd's POST /api/write, which
// runs writes concurrently (the limiter caps bound them) and answers once its
// own state shows the change. A request may carry several operations (Ops),
// applied as one bd transaction where bd can (see txn.go). br stays behind it. A process
// that cannot reach cmdrd (down, no token) runs br directly and logs it;
// COMMANDER_WRITE_DOOR=off skips the door altogether.

// DoorEnv switches the door off when set to off/0/false.
const DoorEnv = "COMMANDER_WRITE_DOOR"

// DoorPath is the cmdrd route that runs one br write.
const DoorPath = "/api/write"

// DoorRequest is the body of POST /api/write: br argv (without the binary) run in Dir.
// Ops, when set, is a list of argv applied as one change (Args is then ignored);
// Atomic refuses the request unless the whole list can be one transaction.
type DoorRequest struct {
	CompletionWaivers []CompletionWaiver `json:"completion_waivers,omitempty"`
	Plan              *PlanBasis         `json:"plan,omitempty"`
	Propose           bool               `json:"propose,omitempty"`
	Dir               string             `json:"dir"`
	Args              []string           `json:"args,omitempty"`
	Ops               [][]string         `json:"ops,omitempty"`
	Atomic            bool               `json:"atomic,omitempty"`
	// NoWait acknowledges the durable mutation without waiting on its UI projection.
	NoWait bool `json:"no_wait,omitempty"`
	// Env is the caller's attribution env (see DoorEnvKeep); cmdrd applies the
	// allowed entries to br so the audit author stays the caller's.
	Env []string `json:"env,omitempty"`
	// Intent asks the door to judge the write as a whole before it runs (the guarded holding
	// layer): it reads the closure of Intent.Bead fresh, simulates the ops on it, refuses a write
	// that would leave a bead with no holder (for every caller, the user too, who alone may pass
	// LeaveUnheld) and runs holder-adding ops first. Without it the door runs the ops as given.
	Intent *DoorIntent `json:"intent,omitempty"`
}

// DoorIntent names what a write is about, for the door's holder check.
type DoorIntent struct {
	Bead        string `json:"bead"`
	Verb        string `json:"verb,omitempty"`
	LeaveUnheld string `json:"leave_unheld,omitempty"` // the user's recorded reason to leave work unheld
}

type intentKey struct{}

// WithIntent marks the writes made under ctx with the door's holder check (DoorRequest.Intent):
// cmdrd judges what they leave on fresh reads, orders holder-adding ops first and lays the
// result on the board at once. A verb puts it on its decisive write (a close, a state flush),
// whose result is the state it promised, never on an intermediate one.
func WithIntent(ctx context.Context, in DoorIntent) context.Context {
	return context.WithValue(ctx, intentKey{}, &in)
}

func intentOf(ctx context.Context) *DoorIntent {
	in, _ := ctx.Value(intentKey{}).(*DoorIntent)
	return in
}

// DoorResponse is its answer. Exit 0 with Error "" is success.
type DoorResponse struct {
	PlanReceipt string            `json:"plan_receipt,omitempty"`
	ProposalID  string            `json:"proposal_id,omitempty"`
	Created     map[string]string `json:"created,omitempty"`
	Replayed    bool              `json:"replayed,omitempty"`
	Stdout      string            `json:"stdout"`
	Stderr      string            `json:"stderr"`
	Exit        int               `json:"exit"`
	Error       string            `json:"error,omitempty"`
	// Busy says the write never started because br stayed busy (ErrBusy), so
	// repeating it is safe.
	Busy bool `json:"busy,omitempty"`
	// MaybeApplied says the write was killed or timed out while it ran: it may
	// have committed before it died, so a caller checks before repeating it.
	MaybeApplied bool `json:"maybe_applied,omitempty"`
	// Settled is false when the daemon's state did not show the change within
	// its wait; the write itself is done.
	Settled bool `json:"settled"`
	// Applied counts the operations that ran; Atomic says they were one transaction.
	Applied           int     `json:"applied,omitempty"`
	Atomic            bool    `json:"atomic,omitempty"`
	BackendMS         float64 `json:"backend_ms,omitempty"`
	ProjectionMS      float64 `json:"projection_ms,omitempty"`
	ProjectionPending bool    `json:"projection_pending,omitempty"`
	// Holders is, for an intent write, what holds each bead of its closure once the write
	// landed ("" = nothing: only with the user's leave_unheld), as the door's simulation read it.
	Holders map[string]string `json:"holders,omitempty"`
}

// DoorEnabled reports whether this process may use the door.
func DoorEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(DoorEnv))) {
	case "off", "0", "false", "no":
		return false
	}
	return true
}

var inDoorServer bool

// SetDoorServer marks this process as the one serving the door (cmdrd): its own
// writes run br directly, in the same per-project line as the door's.
func SetDoorServer(on bool) { inDoorServer = on }

// DoorWritable reports whether args are a br write the door accepts. init stays
// direct: it creates the workspace the door's dir check looks for. A leading flag
// (--db <path>, --x init) is refused so argv cannot redirect br or hide the verb.
func DoorWritable(args []string) bool {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return false
	}
	return isWrite(args) && args[0] != "init" && args[0] != "batch"
}

// DoorEnvKeep reports whether a caller's env var travels through the door. It is
// an allow-list of actor/identity variables (audit author only); nothing that
// could point br or bd at another server, database or binary.
func DoorEnvKeep(kv string) bool {
	name, _, ok := strings.Cut(kv, "=")
	if !ok {
		return false
	}
	switch name {
	case "USER", "LOGNAME", "COMMANDER_ROLE", "BEADS_ACTOR", "BD_ACTOR", "BR_ACTOR", "COMMANDER_RUN_ID", "COMMANDER_RUN_SESSION", "COMMANDER_MANAGER_ROLE":
		return true
	}
	return false
}

type doorEnvKey struct{}

// WithDoorEnv adds the allowed entries of env to br's environment for writes run under ctx.
func WithDoorEnv(ctx context.Context, env []string) context.Context {
	var keep []string
	for _, kv := range env {
		if DoorEnvKeep(kv) {
			keep = append(keep, kv)
		}
	}
	// Even an empty attribution is explicit. Do not inherit a parent request's
	// or daemon process's execution identity when a service sets a new context.
	return context.WithValue(ctx, doorEnvKey{}, keep)
}

// RunWrite runs one br write directly. It is the body of every write made
// without the door. Writes are not queued: the backend takes them concurrently,
// bounded only by the limiter caps.
func RunWrite(ctx context.Context, dir, name string, args ...string) (stdout, stderr []byte, err error) {
	return runBr(ctx, dir, name, args...)
}

// RunBr runs br with args in dir (the door's default write runner).
func RunBr(ctx context.Context, dir string, args ...string) ([]byte, []byte, error) {
	return runBr(ctx, dir, beadsBinary(), args...)
}

// ---- client ----

var doorHTTP = &http.Client{Transport: &http.Transport{DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}

// doorCallTimeout bounds a door call after it connected (br's own 40s plus the
// settle wait).
const doorCallTimeout = 3 * time.Minute

// ErrMaybeApplied marks a write that was sent but not answered: it may have
// run, so a caller checks before repeating it.
var ErrMaybeApplied = errors.New("the write may still have been applied")

// MayHaveApplied reports whether a failed write may have run all the same: its
// answer was lost, or it was killed or timed out while running (the text covers
// a door that sends no MaybeApplied). A busy write never started.
func MayHaveApplied(err error) bool {
	if err == nil || IsBusy(err) {
		return false
	}
	if errors.Is(err, ErrMaybeApplied) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "signal: killed") || strings.Contains(msg, "deadline exceeded")
}

// doorError is the error of a failed DoorResponse, keeping what it says about
// repeating the write.
func doorError(out DoorResponse) error {
	msg := out.Error
	if msg == "" {
		msg = fmt.Sprintf("exit status %d", out.Exit)
	}
	switch {
	case out.Busy:
		return fmt.Errorf("%s: %w", msg, ErrBusy)
	case out.MaybeApplied:
		return fmt.Errorf("%s (%w; check before repeating a create or comment)", msg, ErrMaybeApplied)
	}
	return errors.New(msg)
}

// errDoorDown means the door could not be reached, so nothing was sent.
var errDoorDown = errors.New("write door unreachable")

func callerEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		if DoorEnvKeep(kv) {
			out = append(out, kv)
		}
	}
	return out
}

// viaDoor sends the write to cmdrd. A returned errDoorDown means it did not run.
func viaDoor(ctx context.Context, dir string, req DoorRequest) (DoorResponse, error) {
	home := brHome()
	rawURL, err := os.ReadFile(filepath.Join(home, "api-url"))
	if err != nil {
		return DoorResponse{}, fmt.Errorf("%w: no api-url", errDoorDown)
	}
	rawTok, err := os.ReadFile(filepath.Join(home, "api-token"))
	if err != nil || strings.TrimSpace(string(rawTok)) == "" {
		return DoorResponse{}, fmt.Errorf("%w: no api-token", errDoorDown)
	}
	req.Dir, req.Env = dir, callerEnv()
	req.CompletionWaivers = completionWaiversOf(ctx)
	body, _ := json.Marshal(req)
	cctx, cancel := context.WithTimeout(ctx, doorCallTimeout)
	defer cancel()
	path := DoorPath
	if req.Plan != nil {
		path = PlanPath
		if req.Propose {
			path = PlanProposePath
		}
	}
	hreq, err := http.NewRequestWithContext(cctx, http.MethodPost, strings.TrimRight(strings.TrimSpace(string(rawURL)), "/")+path, bytes.NewReader(body))
	if err != nil {
		return DoorResponse{}, fmt.Errorf("%w: %v", errDoorDown, err)
	}
	hreq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(rawTok)))
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := doorHTTP.Do(hreq)
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) && opErr.Op == "dial" {
			return DoorResponse{}, fmt.Errorf("%w: %v", errDoorDown, err)
		}
		// Sent but unanswered: the write may have run, so never repeat it.
		return DoorResponse{}, fmt.Errorf("write door: %w (%w, e.g. when cmdrd restarted; check before repeating a create or comment)", err, ErrMaybeApplied)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusUnauthorized {
		return DoorResponse{}, fmt.Errorf("%w: http %d", errDoorDown, resp.StatusCode)
	}
	var out DoorResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return DoorResponse{}, fmt.Errorf("write door: bad answer (http %d): %w (%w; check before repeating a create or comment)", resp.StatusCode, err, ErrMaybeApplied)
	}
	if out.Error != "" || out.Exit != 0 {
		return out, doorError(out)
	}
	return out, nil
}

// runWrite is how a br write leaves a client: through the door when it is on and
// cmdrd answers, else directly (logged).
func runWrite(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
	if PostgresConfigured() && !inDoorServer && !DoorEnabled() {
		return nil, nil, fmt.Errorf("Postgres write door cannot be disabled")
	}
	if dir != "" && !inDoorServer && DoorEnabled() && DoorWritable(args) {
		out, err := viaDoor(ctx, dir, DoorRequest{Args: args, NoWait: true, Intent: intentOf(ctx)})
		if !errors.Is(err, errDoorDown) {
			return []byte(out.Stdout), []byte(out.Stderr), err
		}
		if PostgresConfigured() {
			return nil, nil, fmt.Errorf("Postgres writes require the Commander service: %w", err)
		}
		logDoorFallback(dir, args, err)
	}
	stdout, stderr, err := RunWrite(ctx, dir, name, args...)
	if MayHaveApplied(err) {
		err = fmt.Errorf("%w (%w; check before repeating a create or comment)", err, ErrMaybeApplied)
	}
	return stdout, stderr, err
}

var fallbackMu sync.Mutex

// logDoorFallback appends one line to $COMMANDER_HOME/logs/write-door-fallback.jsonl.
func logDoorFallback(dir string, args []string, cause error) {
	fallbackMu.Lock()
	defer fallbackMu.Unlock()
	logs := filepath.Join(brHome(), "logs")
	if os.MkdirAll(logs, 0o755) != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(logs, "write-door-fallback.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	verb := ""
	if len(args) > 0 {
		verb = args[0]
	}
	line, _ := json.Marshal(map[string]any{"time": time.Now().UTC().Format(time.RFC3339), "dir": dir, "verb": verb, "reason": cause.Error()})
	f.Write(append(line, '\n'))
}
