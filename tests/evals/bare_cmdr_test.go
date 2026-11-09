package evals

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/testguard"
)

type bareCmdrCase struct {
	Name        string            `json:"name"`
	Binary      string            `json:"binary"`
	Argv        []string          `json:"argv"`
	Env         map[string]string `json:"env"`
	Terminal    bool              `json:"terminal"`
	StdoutPipe  bool              `json:"stdout_pipe"`
	WantExit    int               `json:"want_exit"`
	WantSession bool              `json:"want_session"`
}

// bareCmdrRig is the real built cmdr with `tmux` on its PATH being a shim that
// logs each call and runs tmux on a private socket with TMUX unset.
type bareCmdrRig struct {
	dir, bin, sock, probeSock, calls, tmux string
	env                                    []string
}

func newBareCmdrRig(t *testing.T) *bareCmdrRig {
	t.Helper()
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	// Short and ours: socket paths over ~104 chars fail. Removed by exact path.
	dir, err := os.MkdirTemp("", "bc")
	if err != nil {
		t.Fatal(err)
	}
	r := &bareCmdrRig{dir: dir, bin: filepath.Join(dir, "bin"), sock: testguard.TmuxSocket(dir, "cockpit"),
		probeSock: testguard.TmuxSocket(dir, "probe"), calls: filepath.Join(dir, "tmux-calls"), tmux: tmux}
	testguard.NoLeakedProcesses(t, dir)
	t.Cleanup(func() {
		r.killServers()
		_ = os.RemoveAll(dir)
	})
	home, ch := filepath.Join(dir, "home"), filepath.Join(dir, "ch")
	for _, d := range []string{r.bin, home, ch, filepath.Dir(r.sock)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	buildEnv := append(os.Environ(), "GOFLAGS=-p=2")
	mustBuild(t, buildEnv, filepath.Join(r.bin, "cmdr"), "./cmd/cmdr")
	mustBuild(t, buildEnv, filepath.Join(r.bin, "opencode"), "./tests/evals/fakeopencode")
	if err := os.Symlink("cmdr", filepath.Join(r.bin, "commander")); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"claude", "codex"} {
		writeExecutable(t, filepath.Join(r.bin, agent), "#!/bin/sh\nexit 1\n")
	}
	writeExecutable(t, filepath.Join(r.bin, "tmux"), fmt.Sprintf("#!/bin/sh\nunset TMUX\necho \"$*\" >> %q\nexec %q -S %q \"$@\"\n", r.calls, tmux, r.sock))
	// An explicit environment: nothing of the caller's (role, TMUX) reaches cmdr.
	r.env = []string{"HOME=" + home, "COMMANDER_HOME=" + ch, "COMMANDER_RUNTIME=opencode", "COMMANDER_CODEX_OPERATOR_SHARE=0",
		"COMMANDER_CODEX_MANAGER_SHARE=0", "AM_HTTP_URL=http://127.0.0.1:1", "TMUX_TMPDIR=" + dir, "TERM=xterm-256color",
		"PATH=" + r.bin + ":/usr/bin:/bin"}
	return r
}

func (r *bareCmdrRig) killServers() {
	testguard.KillTmuxServer(r.dir, "cockpit")
	testguard.KillTmuxServer(r.dir, "probe")
	// The server exits just after that call; a new one must not meet its socket.
	for _, sock := range []string{r.sock, r.probeSock} {
		for i := 0; i < 50 && exec.Command(r.tmux, "-S", sock, "list-sessions").Run() == nil; i++ {
			time.Sleep(50 * time.Millisecond)
		}
		_ = os.Remove(sock)
	}
}

func (r *bareCmdrRig) hasCockpit() bool {
	return exec.Command(r.tmux, "-S", r.sock, "has-session", "-t", "=commander").Run() == nil
}

// waitForClient reports whether a terminal attached to the cockpit in time.
func (r *bareCmdrRig) waitForClient(wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if out, err := exec.Command(r.tmux, "-S", r.sock, "list-clients").Output(); err == nil && len(bytes.TrimSpace(out)) > 0 {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func (r *bareCmdrRig) caseEnv(c bareCmdrCase) []string {
	env := append([]string{}, r.env...)
	for k, v := range c.Env {
		env = append(env, k+"="+v)
	}
	return env
}

// runDetached runs cmdr with stdin and stdout that are no terminal.
func (r *bareCmdrRig) runDetached(t *testing.T, c bareCmdrCase, binary string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), evalTimeout(t, 30*time.Second))
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(r.bin, binary), c.Argv...)
	cmd.Env = r.caseEnv(c)
	var stderr, stdout bytes.Buffer
	cmd.Stderr = &stderr
	if c.StdoutPipe {
		cmd.Stdout = &stdout
	}
	// tmux's attach fails with no terminal; a server keeps the session, and its pipes must not hold Run open.
	cmd.WaitDelay = 3 * time.Second
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil && !strings.Contains(err.Error(), "wait") {
		t.Fatalf("run: %v", err)
	}
	return code, stderr.String()
}

// runInTerminal runs cmdr in a pane of a separate private tmux server, so stdin
// and stdout are a real pty. It returns as soon as the pane wrote an exit code,
// or (ok=false) when cmdr is still running at the deadline: attached, as it stays
// in the user's terminal.
func (r *bareCmdrRig) runInTerminal(t *testing.T, c bareCmdrCase, binary string, wait time.Duration) (int, string, bool) {
	t.Helper()
	exitFile, errFile := filepath.Join(r.dir, "exit"), filepath.Join(r.dir, "stderr")
	_ = os.Remove(exitFile)
	_ = os.Remove(errFile)
	var line strings.Builder
	line.WriteString("cd " + strconv.Quote(r.dir) + "; ")
	for _, kv := range r.caseEnv(c) {
		k, v, _ := strings.Cut(kv, "=")
		line.WriteString("export " + k + "=" + strconv.Quote(v) + "; ")
	}
	line.WriteString("unset TMUX TMUX_PANE; " + binary)
	for _, a := range c.Argv {
		line.WriteString(" " + strconv.Quote(a))
	}
	fmt.Fprintf(&line, " 2>%q; echo $? > %q", errFile, exitFile)
	start := exec.Command(r.tmux, "-S", r.probeSock, "new-session", "-d", "-x", "200", "-y", "50", "-s", "probe", "/bin/sh", "-c", line.String())
	start.Env = r.env
	if out, err := start.CombinedOutput(); err != nil {
		t.Fatalf("start the terminal: %v\n%s", err, out)
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(exitFile); err == nil && len(bytes.TrimSpace(raw)) > 0 {
			code, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
			stderr, _ := os.ReadFile(errFile)
			return code, string(stderr), true
		}
		if c.WantExit == 0 && r.hasCockpit() {
			return 0, "", false
		}
		time.Sleep(100 * time.Millisecond)
	}
	stderr, _ := os.ReadFile(errFile)
	return 0, string(stderr), false
}

func TestBareCmdrScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/bare-cmdr/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []bareCmdrCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	r := newBareCmdrRig(t)
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			r.killServers()
			_ = os.Remove(r.calls)
			binary := c.Binary
			if binary == "" {
				binary = "cmdr"
			}
			var code int
			var stderr string
			if c.Terminal {
				var exited bool
				code, stderr, exited = r.runInTerminal(t, c, binary, evalTimeout(t, 30*time.Second))
				if !exited && !c.WantSession {
					t.Fatalf("cmdr did not exit; stderr=%q", stderr)
				}
			} else {
				code, stderr = r.runDetached(t, c, binary)
			}
			calls, _ := os.ReadFile(r.calls)
			if c.WantSession {
				if !r.hasCockpit() {
					t.Fatalf("no cockpit on the private socket: exit=%d stderr=%q calls=%s", code, stderr, calls)
				}
				if c.Terminal && !r.waitForClient(evalTimeout(t, 10*time.Second)) {
					t.Fatalf("cockpit built but nothing attached: stderr=%q calls=%s", stderr, calls)
				}
				return
			}
			if code != c.WantExit {
				t.Fatalf("exit=%d want %d; stderr=%q", code, c.WantExit, stderr)
			}
			if !strings.Contains(stderr, "cmdr --help") {
				t.Fatalf("stderr does not name cmdr --help: %q", stderr)
			}
			if len(calls) != 0 {
				t.Fatalf("a refusal must make no tmux call, got:\n%s", calls)
			}
			if r.hasCockpit() {
				t.Fatalf("a refusal built a cockpit")
			}
			t.Logf("refused:\n%s", stderr)
		})
	}
}

// cockpit-test.sh creates its session with no terminal: it must keep working
// with the real cmdr, whatever role its caller has.
func TestBareCmdrCockpitTestStillWorks(t *testing.T) {
	r := newBareCmdrRig(t)
	iso := filepath.Join(r.dir, "ct")
	if err := os.MkdirAll(iso, 0o700); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(repoRoot(), "scripts", "cockpit-test.sh")
	script := func(verb string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), evalTimeout(t, 60*time.Second))
		defer cancel()
		cmd := exec.CommandContext(ctx, "bash", scriptPath, verb)
		cmd.Env = []string{"HOME=" + filepath.Join(r.dir, "home"), "PATH=/usr/bin:/bin", "COMMANDER_ROLE=operator-demo-1",
			"COMMANDER_MANAGER_ROLE=manager-demo", "COCKPIT_TMUX_TMPDIR=" + iso,
			"COCKPIT_CMDR=" + filepath.Join(r.bin, "cmdr"), "COCKPIT_FAKE_OPENCODE=" + filepath.Join(r.bin, "opencode")}
		cmd.WaitDelay = 3 * time.Second
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	// Ends the script's own session on its own socket, so its server exits too.
	t.Cleanup(func() { _, _ = script("down") })
	if out, err := script("up"); err != nil || !strings.Contains(out, "up (detached") {
		t.Fatalf("up: %v\n%s", err, out)
	}
	if out, err := script("text"); err != nil || !strings.Contains(out, "===== pane") {
		t.Fatalf("text: %v\n%s", err, out)
	}
}
