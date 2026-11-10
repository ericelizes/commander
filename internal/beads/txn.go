package beads

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Several changes in one request. bd has one transaction mechanism, `bd batch`:
// a narrow script (close, update status/priority/title/assignee, create, dep
// add/remove) run in one Dolt transaction and one commit, rolled back whole on
// any error. A list of operations that all fit it is sent as one batch, so no
// half-done state is ever visible. Anything else (labels, comments, a create
// with a parent or labels, br) has no transaction: with Atomic set the request
// is refused before anything runs; without it the operations run one after the
// other, stop at the first failure, and the answer says how many applied.
// A batch create cannot hand its new id to a later operation of the same batch.

// BatchRunner runs a bd batch script in dir.
type BatchRunner func(ctx context.Context, dir, script string) (stdout, stderr []byte, err error)

// RunBatch runs script through `bd batch`.
func RunBatch(ctx context.Context, dir, script string) ([]byte, []byte, error) {
	return runBrIn(ctx, dir, beadsBinary(), []byte(script), "batch")
}

// BatchBackend reports whether the beads CLI is bd (the only one with batch).
func BatchBackend() bool { return isBD() }

// MaxOps is the most operations one request may carry.
const MaxOps = 50

// ErrNotAtomic means an Atomic request cannot be one transaction.
var ErrNotAtomic = errors.New("these operations cannot be applied as one transaction")

// OpsWritable reports whether ops is a non-empty list of writes the door accepts.
func OpsWritable(ops [][]string) bool {
	if len(ops) == 0 || len(ops) > MaxOps {
		return false
	}
	for _, op := range ops {
		if !DoorWritable(op) {
			return false
		}
	}
	return true
}

// BatchScript translates ops into a bd batch script; ok is false when any
// operation (or flag) is outside batch's grammar.
func BatchScript(ops [][]string) (string, bool) {
	var b strings.Builder
	for _, op := range ops {
		line, ok := batchLine(op)
		if !ok {
			return "", false
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String(), len(ops) > 0
}

func batchQuote(v string) (string, bool) {
	if v == "" || strings.ContainsAny(v, "\n\r\x00") {
		return "", false
	}
	if !strings.ContainsAny(v, " \t\"\\") {
		return v, true
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`, true
}

func batchLine(op []string) (string, bool) {
	if len(op) < 2 {
		return "", false
	}
	var out []string
	add := func(vs ...string) bool {
		for _, v := range vs {
			q, ok := batchQuote(v)
			if !ok {
				return false
			}
			out = append(out, q)
		}
		return true
	}
	switch op[0] {
	case "close":
		// bd batch's own `close` skips the open-children/live-blocker refusal that
		// `bd close` applies, so a close rides as `update status=closed`, which
		// applies it (force=true overrides, as --force does). A close reason has
		// no place in that grammar, so a close with one is not batchable.
		force := false
		for i := 2; i < len(op); i++ {
			if op[i] != "--force" {
				return "", false
			}
			force = true
		}
		if !add("update", op[1], "status=closed") || (force && !add("force=true")) {
			return "", false
		}
	case "update":
		if len(op) < 4 || len(op)%2 != 0 || !add("update", op[1]) {
			return "", false
		}
		for i := 2; i < len(op); i += 2 {
			key := strings.TrimPrefix(op[i], "--")
			switch key {
			case "status", "priority", "title", "assignee":
			default:
				return "", false
			}
			if key == op[i] || !add(key+"="+op[i+1]) {
				return "", false
			}
			if key == "priority" {
				if n, err := strconv.Atoi(op[i+1]); err != nil || n < 0 || n > 4 {
					return "", false
				}
			}
		}
	case "create":
		typ, prio := "task", "2"
		for i := 2; i < len(op); i += 2 {
			if i+1 >= len(op) {
				return "", false
			}
			switch op[i] {
			case "--type", "-t":
				typ = op[i+1]
			case "--priority", "-p":
				if n, err := strconv.Atoi(op[i+1]); err != nil || n < 0 || n > 4 {
					return "", false
				}
				prio = op[i+1]
			default:
				return "", false
			}
		}
		if !add("create", typ, prio, op[1]) {
			return "", false
		}
	case "dep":
		if len(op) < 4 || (op[1] != "add" && op[1] != "remove") || !add("dep", op[1], op[2], op[3]) {
			return "", false
		}
		switch {
		case len(op) == 4:
		case len(op) == 6 && op[1] == "add" && (op[4] == "--type" || op[4] == "-t"):
			if !add(op[5]) {
				return "", false
			}
		default:
			return "", false
		}
	default:
		return "", false
	}
	return strings.Join(out, " "), true
}

// ApplyOps applies ops in dir (see the note above). run executes one argv,
// batch one script; batchOK says batch exists. The answer's Applied and Atomic
// tell what happened; a failure has Exit 1 and Error.
func ApplyOps(ctx context.Context, dir string, ops [][]string, atomic, batchOK bool, run func(ctx context.Context, dir string, args []string) ([]byte, []byte, error), batch BatchRunner) DoorResponse {
	if PostgresConfigured() { return postgresApplyOps(ctx,dir,ops) }
	fail := func(applied int, err error, stdout, stderr []byte) DoorResponse {
		return DoorResponse{Stdout: string(stdout), Stderr: string(stderr), Exit: 1, Error: err.Error(), Applied: applied, Busy: applied == 0 && IsBusy(err), MaybeApplied: !IsBusy(err) && cut(ctx, err)}
	}
	if len(ops) == 1 && !atomic {
		stdout, stderr, err := run(ctx, dir, ops[0])
		if err != nil {
			return fail(0, err, stdout, stderr)
		}
		return DoorResponse{Stdout: string(stdout), Stderr: string(stderr), Applied: 1}
	}
	if batchOK {
		if script, ok := BatchScript(ops); ok {
			stdout, stderr, err := batch(ctx, dir, script)
			if err != nil {
				if cut(ctx, err) {
					// Killed or timed out: bd may have committed before it died.
					return fail(0, fmt.Errorf("batch cut short, it may have been applied (check before repeating a create): %w", err), stdout, stderr)
				}
				return fail(0, fmt.Errorf("nothing applied (rolled back): %w", err), stdout, stderr)
			}
			return DoorResponse{Stdout: string(stdout), Stderr: string(stderr), Applied: len(ops), Atomic: true}
		}
	}
	if atomic {
		return fail(0, fmt.Errorf("%w: %s", ErrNotAtomic, notAtomicWhy(batchOK)), nil, nil)
	}
	var out, errOut strings.Builder
	for i, op := range ops {
		if ctx.Err() != nil {
			return fail(i, fmt.Errorf("deadline reached before operation %d of %d, %d applied (not atomic: those stay applied): %w", i+1, len(ops), i, ctx.Err()), []byte(out.String()), []byte(errOut.String()))
		}
		stdout, stderr, err := run(ctx, dir, op)
		out.Write(stdout)
		errOut.Write(stderr)
		if err != nil {
			return fail(i, fmt.Errorf("operation %d of %d failed after %d applied (not atomic: those stay applied): %w", i+1, len(ops), i, err), []byte(out.String()), []byte(errOut.String()))
		}
	}
	return DoorResponse{Stdout: out.String(), Stderr: errOut.String(), Applied: len(ops)}
}

// cut reports whether err means the process was timed out or killed, so a
// commit may have happened before it died.
func cut(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "signal: killed")
}

func notAtomicWhy(batchOK bool) string {
	if !batchOK {
		return "this backend has no transaction (bd batch needs bd)"
	}
	return "an operation is outside bd batch's grammar (close without a reason, update status/priority/title/assignee, create with title/type/priority, dep add/remove)"
}

// Txn is the client side: apply ops through the door (else directly, logged) as
// one change. With atomic set it refuses rather than apply part of them.
func Txn(ctx context.Context, dir string, ops [][]string, atomic bool) (DoorResponse, error) {
	return TxnIntent(ctx, dir, ops, atomic, nil)
}

// TxnIntent is Txn with the door's holder check (DoorRequest.Intent). When cmdrd cannot be
// reached the ops run directly, unjudged (the same fallback as every door write, logged).
func TxnIntent(ctx context.Context, dir string, ops [][]string, atomic bool, intent *DoorIntent) (DoorResponse, error) {
	if PostgresConfigured() && !inDoorServer && !DoorEnabled() { return DoorResponse{},errors.New("Postgres write door cannot be disabled") }
	if !OpsWritable(ops) {
		return DoorResponse{}, errors.New("beads: Txn needs one or more write operations")
	}
	if dir != "" && !inDoorServer && DoorEnabled() {
		resp, err := viaDoor(ctx, dir, DoorRequest{Ops: ops, Atomic: atomic, NoWait: intent == nil, Intent: intent})
		if !errors.Is(err, errDoorDown) {
			return resp, err
		}
		if PostgresConfigured() { return DoorResponse{},fmt.Errorf("Postgres writes require the Commander service: %w",err) }
		logDoorFallback(dir, ops[0], err)
	}
	run := func(ctx context.Context, dir string, args []string) ([]byte, []byte, error) {
		return RunWrite(ctx, dir, beadsBinary(), args...)
	}
	resp := ApplyOps(ctx, dir, ops, atomic, BatchBackend(), run, RunBatch)
	if resp.Exit != 0 {
		return resp, doorError(resp)
	}
	return resp, nil
}
