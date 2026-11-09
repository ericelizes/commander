package docker

import (
	"context"
	"strings"
	"testing"
)

// recorder captures the argv from one exec call so tests can assert on it.
type recorder struct {
	args   []string
	stdout []byte
	stderr []byte
	err    error
}

func (r *recorder) fn() execFunc {
	return func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
		r.args = append([]string{name}, args...)
		return r.stdout, r.stderr, r.err
	}
}

// argvHas returns true iff every needle appears as a contiguous subsequence
// or as a single token in argv.
func argvHasToken(args []string, token string) bool {
	for _, a := range args {
		if a == token {
			return true
		}
	}
	return false
}

// argvHasFlag returns true iff `flag value` appears as adjacent tokens.
func argvHasFlag(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestSpawn_Darwin_NetworkAuto(t *testing.T) {
	r := &recorder{stdout: []byte("abc123\n")}
	c := &cmdClient{exec: r.fn(), OS: "darwin"}

	id, err := c.Spawn(context.Background(), SpawnOptions{
		Image:   "commander/operator:latest",
		Name:    "cmdr-proj-feature-x-abc",
		Network: NetworkAuto,
		Volumes: []VolumeMount{
			{Source: "/host/code", Target: "/workspace"},
			{Source: "/host/cfg", Target: "/root/.config/opencode", ReadOnly: true},
		},
		Labels: map[string]string{
			"commander.project": "proj",
			"commander.task":    "task-1",
			"commander.run-id":  "42",
		},
		Cmd: []string{"opencode", "run"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if id != "abc123" {
		t.Errorf("id = %q; want abc123", id)
	}

	// Darwin: NO --network host
	if argvHasFlag(r.args, "--network", "host") {
		t.Errorf("darwin must not pass --network host: %v", r.args)
	}
	// Darwin: HAS --add-host
	if !argvHasToken(r.args, "--add-host=host.docker.internal:host-gateway") {
		t.Errorf("darwin must pass --add-host: %v", r.args)
	}
	// Darwin: injected AM_HOST/AM_PORT
	if !argvHasFlag(r.args, "-e", "AM_HOST=host.docker.internal") {
		t.Errorf("darwin must inject AM_HOST: %v", r.args)
	}
	if !argvHasFlag(r.args, "-e", "AM_PORT=8765") {
		t.Errorf("darwin must inject AM_PORT: %v", r.args)
	}
	// Labels present
	if !argvHasFlag(r.args, "--label", "commander.project=proj") {
		t.Errorf("missing project label: %v", r.args)
	}
	if !argvHasFlag(r.args, "--label", "commander.task=task-1") {
		t.Errorf("missing task label: %v", r.args)
	}
	if !argvHasFlag(r.args, "--label", "commander.run-id=42") {
		t.Errorf("missing run-id label: %v", r.args)
	}
	// :ro flag on read-only mount
	if !argvHasFlag(r.args, "-v", "/host/cfg:/root/.config/opencode:ro") {
		t.Errorf("missing :ro mount: %v", r.args)
	}
	// Plain mount has no :ro
	if !argvHasFlag(r.args, "-v", "/host/code:/workspace") {
		t.Errorf("missing rw mount: %v", r.args)
	}
	// --rm and -d
	if !argvHasToken(r.args, "--rm") {
		t.Errorf("missing --rm: %v", r.args)
	}
	if !argvHasToken(r.args, "-d") {
		t.Errorf("missing -d: %v", r.args)
	}
	// --name
	if !argvHasFlag(r.args, "--name", "cmdr-proj-feature-x-abc") {
		t.Errorf("missing --name: %v", r.args)
	}
	// Image and cmd at the end, in that order.
	imgIdx := -1
	for i, a := range r.args {
		if a == "commander/operator:latest" {
			imgIdx = i
			break
		}
	}
	if imgIdx < 0 {
		t.Fatalf("image not in argv: %v", r.args)
	}
	if imgIdx+2 >= len(r.args) || r.args[imgIdx+1] != "opencode" || r.args[imgIdx+2] != "run" {
		t.Errorf("cmd not appended after image: %v", r.args)
	}
}

func TestSpawn_Linux_NetworkAuto(t *testing.T) {
	r := &recorder{stdout: []byte("def456\n")}
	c := &cmdClient{exec: r.fn(), OS: "linux"}

	_, err := c.Spawn(context.Background(), SpawnOptions{
		Image:   "img",
		Name:    "name",
		Network: NetworkAuto,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if !argvHasFlag(r.args, "--network", "host") {
		t.Errorf("linux must pass --network host: %v", r.args)
	}
	if argvHasToken(r.args, "--add-host=host.docker.internal:host-gateway") {
		t.Errorf("linux must NOT pass --add-host on auto: %v", r.args)
	}
	// Linux: no injected AM_HOST (host network reaches localhost directly).
	for _, a := range r.args {
		if strings.HasPrefix(a, "AM_HOST=") {
			t.Errorf("linux must not inject AM_HOST: %v", r.args)
		}
	}
}

func TestSpawn_CallerEnvWinsOverInjection(t *testing.T) {
	r := &recorder{stdout: []byte("xyz\n")}
	c := &cmdClient{exec: r.fn(), OS: "darwin"}

	_, err := c.Spawn(context.Background(), SpawnOptions{
		Image:   "img",
		Name:    "n",
		Network: NetworkAuto,
		Env: map[string]string{
			"AM_HOST": "10.0.0.5",
			"AM_PORT": "9999",
		},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if !argvHasFlag(r.args, "-e", "AM_HOST=10.0.0.5") {
		t.Errorf("caller AM_HOST should win: %v", r.args)
	}
	if !argvHasFlag(r.args, "-e", "AM_PORT=9999") {
		t.Errorf("caller AM_PORT should win: %v", r.args)
	}
	// Make sure we didn't ALSO emit the default — exactly one -e AM_HOST.
	count := 0
	for i := 0; i < len(r.args)-1; i++ {
		if r.args[i] == "-e" && strings.HasPrefix(r.args[i+1], "AM_HOST=") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one AM_HOST env, got %d in %v", count, r.args)
	}
}

func TestSpawn_NetworkHostExplicit(t *testing.T) {
	r := &recorder{stdout: []byte("zzz\n")}
	c := &cmdClient{exec: r.fn(), OS: "darwin"} // even on darwin, explicit wins

	_, err := c.Spawn(context.Background(), SpawnOptions{
		Image: "img", Name: "n", Network: NetworkHost,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if !argvHasFlag(r.args, "--network", "host") {
		t.Errorf("explicit NetworkHost on darwin must still pass --network host: %v", r.args)
	}
	if argvHasToken(r.args, "--add-host=host.docker.internal:host-gateway") {
		t.Errorf("explicit NetworkHost must NOT add host gateway: %v", r.args)
	}
}

func TestSpawn_RequiresImageAndName(t *testing.T) {
	c := &cmdClient{exec: (&recorder{}).fn(), OS: "linux"}
	if _, err := c.Spawn(context.Background(), SpawnOptions{Name: "x"}); err == nil {
		t.Error("expected error for missing Image")
	}
	if _, err := c.Spawn(context.Background(), SpawnOptions{Image: "x"}); err == nil {
		t.Error("expected error for missing Name")
	}
}

func TestSpawn_EmptyStdoutErrors(t *testing.T) {
	r := &recorder{stdout: []byte("\n")}
	c := &cmdClient{exec: r.fn(), OS: "linux"}
	if _, err := c.Spawn(context.Background(), SpawnOptions{Image: "i", Name: "n"}); err == nil {
		t.Error("expected error for empty stdout")
	}
}
