// Package beads is a thin wrapper around the beads CLI.
//
// Every shape invoked here is documented in share/contracts.md. Drift
// from that contract should fail integration tests, not silently break.
package beads

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Bead is the minimal projection of a beads issue commander cares about.
type Bead struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	// AcceptanceCriteria is what counts as passed (`br update --acceptance`);
	// on a gate bead it is the gate's criteria in the derived diagram.
	AcceptanceCriteria string   `json:"acceptance_criteria,omitempty"`
	Status             string   `json:"status"`
	Type               string   `json:"type"`
	Labels             []string `json:"labels"`
	ParentID           string   `json:"parent_id,omitempty"`
	// Parent is the parent `br list` reports. A move does not always touch
	// updated_at, so the detail cache keys on it.
	Parent        string `json:"parent,omitempty"`
	AssignedAgent string `json:"assigned_agent,omitempty"`
	Priority      int    `json:"priority,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	CloseReason   string `json:"close_reason,omitempty"`
	// DependencyCount counts every edge, parent-child included; `br list`
	// carries only the count, Deps fetches the edges.
	Dependencies    []Dep `json:"dependencies,omitempty"`
	DependencyCount int   `json:"dependency_count,omitempty"`
	DependentCount  int   `json:"dependent_count,omitempty"`
	CommentCount    int   `json:"comment_count,omitempty"`
}

// Dep is one edge from `br dep list <id> --json`: the issue depends on DependsOnID.
type Dep struct {
	IssueID     string `json:"issue_id"`
	DependsOnID string `json:"depends_on_id"`
	Type        string `json:"type"` // blocks, parent-child, related, tracks, discovered-from
}

// Detail is what `br show --json` adds to a listed bead.
type Detail struct {
	// Parent is the bead it is a child of by its parent-child edge ("" for
	// none). A bead can be moved under another one, so its dotted id is not
	// always the parent.
	Parent   string
	Deps     []Dep // every edge, parent-child included
	Comments []Comment
}

// Comment is one entry of `br comments list <id> --json`.
type Comment struct {
	ID        string `json:"id"`
	Author    string `json:"author"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"` // RFC3339
}

// ListOptions controls `br list`. The wrapper always emits --json.
type ListOptions struct {
	// Labels are AND-combined (one --label per entry).
	Labels []string
	// LabelAny are OR-combined (one --label-any per entry).
	LabelAny []string
	Status   string
	Type     string
	Limit    int
	// IDs keeps only these beads (`--id`); empty means no filter. A bead that does
	// not exist is simply not listed, so the answer alone cannot tell a missing bead
	// from a short read: a decision must confirm with Show (ErrNotFound). One process,
	// however many are named.
	IDs []string
}

// CreateOptions controls `br create`.
type CreateOptions struct {
	Parent   string
	Labels   []string
	Priority int
	Type     string
	// Description becomes the bead body (`br create --description`).
	Description string
	// AcceptanceCriteria becomes `br create --acceptance`.
	AcceptanceCriteria string
	// Silent makes br print only the new bead ID on stdout.
	Silent bool
}

// Client is the surface commander code depends on. The MockClient in
// mock.go satisfies this for downstream tests.
type Client interface {
	// Init creates the beads workspace in projectDir (idempotent). Must run
	// before Create on a fresh project.
	Init(ctx context.Context, projectDir string) error
	SetState(ctx context.Context, projectDir, issueID, dimension, value, reason string) error
	List(ctx context.Context, projectDir string, opts ListOptions) ([]Bead, error)
	Create(ctx context.Context, projectDir, title string, opts CreateOptions) (string, error)
	Link(ctx context.Context, projectDir, fromID, toID, linkType string) error
	Show(ctx context.Context, projectDir, issueID string) (*Bead, error)
	Rename(ctx context.Context, projectDir, issueID, title string) error
	Close(ctx context.Context, projectDir, issueID string) error
	// Delete tombstones a bead that never was work (`br delete --reason`).
	Delete(ctx context.Context, projectDir, issueID, reason string) error
	SetStatus(ctx context.Context, projectDir, issueID, status string) error
	SetLabel(ctx context.Context, projectDir, issueID, label string, on bool) error
	// SetParent moves issueID under parent by its parent-child edge (`br update --parent`); "" removes it.
	SetParent(ctx context.Context, projectDir, issueID, parent string) error
	// DependOn records that issueID waits on dependsOn (`br dep add`).
	DependOn(ctx context.Context, projectDir, issueID, dependsOn string) error
	// RemoveDep drops the edge DependOn made (`br dep remove`).
	RemoveDep(ctx context.Context, projectDir, issueID, dependsOn string) error
	// UpdateFields sets the text fields and priority of a bead (`br update`).
	UpdateFields(ctx context.Context, projectDir, issueID string, f Fields) error
	// CloseWithReason is Close with the reason recorded (`br close --reason`).
	CloseWithReason(ctx context.Context, projectDir, issueID, reason string) error
	// Deps lists what issueID depends on (`br dep list <id> --json`).
	Deps(ctx context.Context, projectDir, issueID string) ([]Dep, error)
	// Dependents is the reverse edges of issueID: what waits on it or is its child.
	Dependents(ctx context.Context, projectDir, issueID string) ([]Dep, error)
	// Details is the edges and comments of each id, from one batched `br show`
	// instead of a `br dep list` and a `br comments list` per bead. Ids br does
	// not know are missing from the map.
	Details(ctx context.Context, projectDir string, ids []string) (map[string]Detail, error)
	// AddComment appends a comment (`br comments add`); Comments lists them oldest first.
	AddComment(ctx context.Context, projectDir, issueID, text string) error
	Comments(ctx context.Context, projectDir, issueID string) ([]Comment, error)
}

// execFunc abstracts process execution so tests can capture argv without
// running a real beads binary.
type execFunc func(ctx context.Context, dir, name string, args ...string) (stdout, stderr []byte, err error)

// cmdClient is the real Client backed by the beads binary on PATH.
type cmdClient struct {
	run execFunc
}

// New returns a Client that shells out to the configured beads CLI.
func New() Client {
	if PostgresConfigured() {
		return &postgresClient{cmdClient: &cmdClient{run: defaultRunInDir}}
	}
	return &cmdClient{run: defaultRunInDir}
}

// Binary is the beads CLI to run: $COMMANDER_BEADS_BIN, else upstream bd.
func Binary() string { return beadsBinary() }

func beadsBinary() string {
	if v := strings.TrimSpace(os.Getenv("COMMANDER_BEADS_BIN")); v != "" {
		return v
	}
	return "bd"
}

// isBD is true when the beads CLI is upstream bd (Dolt-backed) instead of br.
// The two take the same commands except where noted at each call.
func isBD() bool {
	return !PostgresConfigured() && filepath.Base(beadsBinary()) == "bd"
}

// defaultRun runs name+args via os/exec, capturing stdout and stderr separately.
func defaultRun(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	return defaultRunInDir(ctx, "", name, args...)
}

// brCallTimeout bounds one br call; a var so a test can shorten it.
var brCallTimeout = 40 * time.Second

// defaultRunInDir runs br; a write goes through the write door (door.go).
func defaultRunInDir(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
	if isWrite(args) {
		return runWrite(ctx, dir, name, args...)
	}
	return runBr(ctx, dir, name, args...)
}

func runBr(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
	return runBrIn(ctx, dir, name, nil, args...)
}

// runBrIn is runBr with stdin (bd batch reads its operations there).
func runBrIn(ctx context.Context, dir, name string, stdin []byte, args ...string) ([]byte, []byte, error) {
	if PostgresConfigured() {
		return postgresRun(ctx, dir, args)
	}
	var stdout, stderr []byte
	err := limitedRun(ctx, dir, args, func() error {
		// The timeout starts here, once the call is out of the queue.
		runCtx, cancel := context.WithTimeout(ctx, brCallTimeout)
		defer cancel()
		cmd := exec.CommandContext(runCtx, name, args...)
		if dir != "" {
			cmd.Dir = dir
		}
		var outBuf, errBuf bytes.Buffer
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		cmd.Stdout = &outBuf
		cmd.Stderr = &errBuf
		// Anything br starts under our slot must not take another.
		cmd.Env = append(os.Environ(), HeldEnv+"=1")
		if extra, ok := ctx.Value(doorEnvKey{}).([]string); ok {
			cmd.Env = append(cmd.Env, extra...)
		}
		err := cmd.Run()
		if errors.Is(err, exec.ErrNotFound) {
			err = fmt.Errorf("beads CLI %q not found on PATH (set COMMANDER_BEADS_BIN to its full path): %w", name, err)
		}
		if err != nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("%w: %w", context.DeadlineExceeded, err)
		}
		stdout, stderr = outBuf.Bytes(), errBuf.Bytes()
		return err
	})
	return stdout, stderr, err
}
