package docker

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Spawn assembles `docker run --rm -d --name <name> ...` argv per
// SpawnOptions and returns the container ID printed on stdout.
//
// Per-OS network behavior (NetworkAuto):
//   - Linux: --network host
//   - Darwin: --add-host=host.docker.internal:host-gateway, plus injected
//     AM_HOST=host.docker.internal AM_PORT=8765 env (caller-overridable
//     via opts.Env — caller wins).
//
// Env order and label order are deterministic (sorted by key) so argv
// snapshots in tests are stable. Volume order is preserved from caller.
func (c *cmdClient) Spawn(ctx context.Context, opts SpawnOptions) (string, error) {
	if opts.Image == "" {
		return "", fmt.Errorf("docker: Spawn requires Image")
	}
	if opts.Name == "" {
		return "", fmt.Errorf("docker: Spawn requires Name")
	}

	// Always pass --rm: commander never wants stopped-but-present operator
	// containers cluttering `docker ps -a`. The AutoRemove field is kept
	// in the API for documentation and future flexibility, but every
	// commander spawn site sets it true today.
	args := []string{"run", "--rm", "-d", "--name", opts.Name}

	// Network flags. Resolve NetworkAuto against the host OS first.
	mode := opts.Network
	if mode == NetworkAuto {
		if c.OS == "darwin" {
			mode = NetworkBridge
		} else {
			mode = NetworkHost
		}
	}

	// Env starts as a copy of opts.Env so we can inject AM_HOST/AM_PORT
	// defaults for Darwin without mutating the caller's map.
	env := make(map[string]string, len(opts.Env))
	for k, v := range opts.Env {
		env[k] = v
	}

	switch mode {
	case NetworkHost:
		args = append(args, "--network", "host")
	case NetworkBridge:
		args = append(args, "--add-host=host.docker.internal:host-gateway")
		// Caller wins: only inject defaults if not already set.
		if _, ok := env["AM_HOST"]; !ok {
			env["AM_HOST"] = "host.docker.internal"
		}
		if _, ok := env["AM_PORT"]; !ok {
			env["AM_PORT"] = "8765"
		}
	}

	// Volumes — preserve caller order (mounts can be order-sensitive when
	// nesting paths). :ro suffix when ReadOnly.
	for _, v := range opts.Volumes {
		spec := v.Source + ":" + v.Target
		if v.ReadOnly {
			spec += ":ro"
		}
		args = append(args, "-v", spec)
	}

	// Env — sorted by key for stable argv.
	envKeys := make([]string, 0, len(env))
	for k := range env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		args = append(args, "-e", k+"="+env[k])
	}

	// Labels — sorted by key for stable argv.
	labelKeys := make([]string, 0, len(opts.Labels))
	for k := range opts.Labels {
		labelKeys = append(labelKeys, k)
	}
	sort.Strings(labelKeys)
	for _, k := range labelKeys {
		args = append(args, "--label", k+"="+opts.Labels[k])
	}

	args = append(args, opts.Image)
	args = append(args, opts.Cmd...)

	stdout, stderr, err := c.exec(ctx, "docker", args...)
	if err != nil {
		return "", fmt.Errorf("docker run: %w: %s", err, strings.TrimSpace(string(stderr)))
	}

	// docker run -d prints the full container ID on stdout, one line.
	id := strings.TrimSpace(strings.SplitN(string(stdout), "\n", 2)[0])
	if id == "" {
		return "", fmt.Errorf("docker run: empty container ID on stdout")
	}
	return id, nil
}
