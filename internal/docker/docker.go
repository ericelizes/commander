// Package docker is a thin wrapper around the `docker` CLI.
//
// Every shape invoked here is documented in share/contracts.md. Drift
// from that contract should fail integration tests, not silently break.
//
// Commander uses this wrapper for operator container lifecycle (U9 spawn,
// U10 kill, orphan reaper) and for runtime stats display in the TUI.
package docker

import (
	"bytes"
	"context"
	"os/exec"
	"runtime"
	"time"
)

// VolumeMount describes a single -v argument. ReadOnly appends ":ro".
type VolumeMount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// NetworkMode selects how `docker run` is networked.
//
// NetworkAuto picks the right behavior per host OS:
//   - Linux: `--network host` (operator can reach host am on 127.0.0.1)
//   - Darwin: `--add-host=host.docker.internal:host-gateway` plus injected
//     AM_HOST/AM_PORT env so the operator dials the host via that DNS name.
//
// NetworkHost and NetworkBridge force a specific behavior regardless of OS.
type NetworkMode int

const (
	// NetworkAuto picks per-OS defaults (see NetworkMode docs).
	NetworkAuto NetworkMode = iota
	// NetworkHost forces `--network host`.
	NetworkHost
	// NetworkBridge forces bridge networking with host.docker.internal
	// resolution via --add-host=host-gateway.
	NetworkBridge
)

// SpawnOptions are the inputs to Spawn.
//
// Detach is implicit: commander always runs `docker run -d`. AutoRemove maps
// to `--rm` (default true; commander manages container lifetime explicitly
// via Stop, but --rm guarantees no zombie containers if the host crashes).
//
// Cmd, when non-empty, overrides the image entrypoint and is appended after
// the image argument (e.g. ["opencode", "run", "--agent", "operator-code", ...]).
type SpawnOptions struct {
	Image      string
	Name       string
	Volumes    []VolumeMount
	Env        map[string]string
	Labels     map[string]string
	Network    NetworkMode
	AutoRemove bool
	Cmd        []string
}

// ContainerInfo is the projection of `docker inspect` commander cares about.
type ContainerInfo struct {
	ID        string
	Name      string
	Image     string
	Status    string
	Labels    map[string]string
	CreatedAt time.Time
}

// RuntimeStats is the projection of `docker stats` commander renders in
// the TUI side panel.
type RuntimeStats struct {
	CPUPercent  float64
	MemoryUsed  int64
	MemoryLimit int64
}

// Client is the surface commander code depends on. The MockClient in
// mock.go satisfies this for downstream tests.
type Client interface {
	// Spawn runs `docker run --rm -d --name <name> ... <image> [cmd...]`
	// and returns the container ID printed on stdout.
	Spawn(ctx context.Context, opts SpawnOptions) (string, error)

	// Stop runs `docker stop <id>`. Idempotent: a "No such container"
	// stderr is treated as success so callers can blindly stop on cleanup.
	Stop(ctx context.Context, idOrName string) error

	// Inspect runs `docker inspect <id>`. Returns (nil, nil) when the
	// container does not exist (NOT an error) — this lets the orphan
	// reaper race the container exiting without log spam.
	Inspect(ctx context.Context, idOrName string) (*ContainerInfo, error)

	// Stats runs `docker stats <id> --no-stream --format json` and parses
	// CPU/memory for the side panel.
	Stats(ctx context.Context, idOrName string) (*RuntimeStats, error)

	// ListByLabel runs `docker ps --filter label=<labelKey> --format json`.
	// Returns a possibly-empty slice (never nil) — used by the orphan reaper
	// to find every container commander is responsible for.
	ListByLabel(ctx context.Context, labelKey string) ([]ContainerInfo, error)
}

// execFunc abstracts process execution so tests can capture argv without
// running a real docker binary. Same shape as the U5-a/U5-b/U5-c wrappers.
type execFunc func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)

// cmdClient is the real Client backed by the docker binary on PATH.
//
// OS defaults to runtime.GOOS at construction time but is exported so tests
// can flip it without monkey-patching globals. spawn.go branches on this for
// per-OS network flags.
type cmdClient struct {
	exec execFunc
	OS   string
}

// New returns a Client that shells out to `docker`.
//
// IMPORTANT: Do NOT volume-mount the host's bd or am binaries into operator
// containers — they are Mach-O on macOS and cannot execute on Linux Alpine.
// The operator image installs its own Linux bd/am at build time. See
// share/contracts.md "Decision 6 — bd binary inside operator container".
func New() Client {
	return &cmdClient{
		exec: defaultRun,
		OS:   runtime.GOOS,
	}
}

// defaultRun runs name+args via os/exec, capturing stdout and stderr separately.
func defaultRun(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}
