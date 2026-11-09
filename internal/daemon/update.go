package daemon

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/example-user/commander/internal/api"
	"github.com/example-user/commander/internal/binstage"
	"github.com/example-user/commander/internal/slot"
	"github.com/example-user/commander/internal/updatecheck"
)

// promptTimeout is how long the confirmation waits; giving up counts as Cancel.
const promptTimeout = 120

// repoResolver finds the checkout (a var so tests can point it at a temp dir).
var repoResolver = resolveCommanderRepo

// newUpdater wires the app's install button with the real steps, or nil when
// the checkout is unknown. Nothing here runs until a person confirms the dialog.
func (d *Daemon) newUpdater() *api.Updater {
	repo := repoResolver()
	if repo == "" {
		return nil
	}
	return d.updaterFor(repo, binstage.InstallDir(os.Getenv, userHome()), localBinDir())
}

func (d *Daemon) updaterFor(repo, installDir, localBin string) *api.Updater {
	home := d.home
	return &api.Updater{
		Home: home, Repo: repo, InstallDir: installDir, LocalBin: localBin,
		Check: func(ctx context.Context) updatecheck.Status {
			return updatecheck.Check(ctx, updatecheck.Options{Repo: repo, InstallDir: installDir, BeadsDir: filepath.Join(repo, ".beads")})
		},
		PromptAvailable: PromptAvailable,
		Prompt:          Prompt,
		Export:          exportCommit,
		Build: func(ctx context.Context, src, stageDir string, out io.Writer) error {
			cmd := []string{"bash", filepath.Join(src, "scripts", "stage.sh")}
			code, err := slot.RunEnv(ctx, home, cmd, 30*time.Minute, out, out, buildEnv(os.Environ(), stageDir))
			if err != nil {
				return err
			}
			if code != 0 {
				return fmt.Errorf("stage.sh exited %d", code)
			}
			return nil
		},
		BinaryRevision: binaryRevision,
		Install:        binstage.Install,
		Rollback:       binstage.Rollback,
		Restart:        restartInstalled(installDir),
		Busy: func() string {
			if len(slot.Holders(home, slot.Count(home))) > 0 {
				return "a build slot is held (" + strings.ReplaceAll(slot.Status(home), "\n", "; ") + "); try again when it is free"
			}
			return ""
		},
		Branch: func(ctx context.Context, repo string) (string, error) {
			out, err := updatecheck.ExecGit(ctx, repo, "rev-parse", "--abbrev-ref", "HEAD")
			return strings.TrimSpace(out), err
		},
	}
}

func userHome() string { h, _ := os.UserHomeDir(); return h }

func localBinDir() string { return filepath.Join(userHome(), ".local", "bin") }

// restartInstalled runs the INSTALLED `cmdr install --restart-only --yes`
// detached (own session), so the restart outlives this daemon's exit. The
// channel gets its result (nil = exit 0), so a failed restart is noticed.
func restartInstalled(installDir string) func(io.Writer) (<-chan error, error) {
	return func(out io.Writer) (<-chan error, error) {
		cmd := exec.Command(filepath.Join(installDir, "cmdr"), "install", "--restart-only", "--yes")
		cmd.Env = append(os.Environ(), "COMMANDER_ROLE=")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		logf, err := os.OpenFile(filepath.Join(os.TempDir(), fmt.Sprintf("cmdr-restart-%d.log", os.Getpid())), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err == nil {
			cmd.Stdout, cmd.Stderr = logf, logf
		}
		if err := cmd.Start(); err != nil {
			if logf != nil {
				logf.Close()
			}
			return nil, err
		}
		fmt.Fprintf(out, "started cmdr install --restart-only (pid %d)\n", cmd.Process.Pid)
		done := make(chan error, 1)
		go func() {
			werr := cmd.Wait()
			if logf != nil {
				logf.Close()
			}
			done <- werr
		}()
		return done, nil
	}
}

// exportCommit checks out exactly the commit target into dir as a detached
// worktree: tracked files at that commit only, so untracked files, a go.work or
// uncommitted edits in the live checkout are not part of the build. Hooks are
// disabled for the checkout. The cleanup removes the worktree and its record.
func exportCommit(ctx context.Context, repo, target, dir string) (func(), error) {
	if len(target) != 40 || strings.Trim(target, "0123456789abcdef") != "" {
		return nil, fmt.Errorf("%q is not a full commit hash", target)
	}
	_ = os.RemoveAll(dir)
	_, _ = updatecheck.ExecGit(ctx, repo, "worktree", "prune")
	cleanup := func() {
		bg, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, _ = updatecheck.ExecGit(bg, repo, "worktree", "remove", "--force", dir)
		_ = os.RemoveAll(dir)
		_, _ = updatecheck.ExecGit(bg, repo, "worktree", "prune")
	}
	if _, err := updatecheck.ExecGit(ctx, repo, "-c", "core.hooksPath=/dev/null", "worktree", "add", "--detach", dir, target); err != nil {
		cleanup()
		return nil, err
	}
	return cleanup, nil
}

// buildEnv is the environment of the build: the parent's, minus anything that
// changes what Go builds (GOFLAGS, go.work, the go env file), and the stage dir.
func buildEnv(base []string, stageDir string) []string {
	var env []string
	for _, kv := range base {
		switch strings.SplitN(kv, "=", 2)[0] {
		case "GOFLAGS", "GOWORK", "GOENV", "CMDR_STAGE_DIR":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GOFLAGS=", "GOWORK=off", "GOENV=off", "CMDR_STAGE_DIR="+stageDir)
}

// binaryRevision reads the vcs.revision and vcs.modified built into a binary.
func binaryRevision(path string) (string, bool, error) {
	bi, err := buildinfo.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	var rev string
	var modified bool
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	return rev, modified, nil
}

// ---- native confirmation ----

var errNoGUI = errors.New("no dialog tool found")

// PromptAvailable reports why no native dialog can be shown, or nil.
func PromptAvailable() error {
	if runtime.GOOS == "darwin" {
		_, err := exec.LookPath("osascript")
		return err
	}
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return errors.New("no display")
	}
	for _, tool := range []string{"zenity", "kdialog"} {
		if _, err := exec.LookPath(tool); err == nil {
			return nil
		}
	}
	return errNoGUI
}

// Prompt shows the confirmation (default button Cancel, a timeout is Cancel).
func Prompt(ctx context.Context, title, text string) (bool, error) {
	argv, parse := promptCommand(runtime.GOOS, title, text, exec.LookPath)
	if argv == nil {
		return false, errNoGUI
	}
	ctx, cancel := context.WithTimeout(ctx, (promptTimeout+15)*time.Second)
	defer cancel()
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout = &stdout
	err := cmd.Run()
	return parse(stdout.String(), err), nil
}

// promptCommand builds the dialog command line (text goes through argv, never
// into a script, so commit subjects cannot inject anything) and a parser that
// says whether the output means the person pressed Install.
func promptCommand(goos, title, text string, look func(string) (string, error)) ([]string, func(out string, err error) bool) {
	if goos == "darwin" {
		script := []string{"on run argv",
			fmt.Sprintf(`display dialog (item 2 of argv) with title (item 1 of argv) buttons {"Cancel", "Install"} default button "Cancel" cancel button "Cancel" with icon caution giving up after %d`, promptTimeout),
			"end run"}
		argv := []string{"osascript"}
		for _, l := range script {
			argv = append(argv, "-e", l)
		}
		argv = append(argv, title, text)
		return argv, func(out string, err error) bool {
			return err == nil && strings.Contains(out, "button returned:Install") && strings.Contains(out, "gave up:false")
		}
	}
	if _, err := look("zenity"); err == nil {
		return []string{"zenity", "--question", "--no-markup", "--title", title, "--text", text, "--ok-label", "Install", "--cancel-label", "Cancel", fmt.Sprintf("--timeout=%d", promptTimeout)},
			func(_ string, err error) bool { return err == nil }
	}
	if _, err := look("kdialog"); err == nil {
		return []string{"kdialog", "--title", title, "--yes-label", "Install", "--no-label", "Cancel", "--yesno", text},
			func(_ string, err error) bool { return err == nil }
	}
	return nil, nil
}
