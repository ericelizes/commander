// Package reviewrun records original review execution before releasing its child.
package reviewrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/example-user/commander/internal/reviewpolicy"
)

type Request struct {
	Repo, Base, Task, Actor, PromptFile, Dir string
	RunID                                    int64
	Registry                                 string
	Full                                     bool
	Command                                  []string
}

type Manifest struct {
	Version    string              `json:"version"`
	Task       string              `json:"task"`
	RunID      int64               `json:"run_id"`
	Actor      string              `json:"actor"`
	Policy     reviewpolicy.Result `json:"policy"`
	PromptHash string              `json:"prompt_sha256"`
	Command    []string            `json:"command"`
	Created    time.Time           `json:"created_at"`
}

type Result struct {
	ManifestHash string    `json:"manifest_hash"`
	PID          int       `json:"pid"`
	Exit         int       `json:"exit"`
	Execution    string    `json:"execution"`
	Started      time.Time `json:"started_at"`
	Finished     time.Time `json:"finished_at"`
	StdoutHash   string    `json:"stdout_sha256"`
	StderrHash   string    `json:"stderr_sha256"`
}

func digest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

func seal(path string, v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	b = append(b, '\n')
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
	if err != nil {
		return "", err
	}
	_, writeErr := f.Write(b)
	closeErr := f.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return digest(b), nil
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Run makes no source verdict: exit zero attests only successful execution.
func Run(ctx context.Context, req Request) (Result, error) {
	var result Result
	if req.Task == "" || req.RunID <= 0 || req.Actor == "" || len(req.Command) == 0 {
		return result, fmt.Errorf("task, actual run ID, actor and command required")
	}
	if _, ok := ctx.Deadline(); !ok {
		return result, fmt.Errorf("review execution requires a deadline")
	}
	policy, err := reviewpolicy.Inspect(ctx, req.Repo, req.Base, "HEAD", req.Full)
	if err != nil {
		return result, err
	}
	status := exec.CommandContext(ctx, "git", "-C", req.Repo, "status", "--porcelain", "--untracked-files=no")
	if out, err := status.Output(); err != nil || len(out) > 0 {
		return result, fmt.Errorf("review execution requires a clean committed tree")
	}
	prompt, err := os.ReadFile(req.PromptFile)
	if err != nil {
		return result, err
	}
	if !filepath.IsAbs(req.Dir) {
		return result, fmt.Errorf("run directory must be absolute")
	}
	if err = os.Mkdir(req.Dir, 0700); err != nil {
		return result, fmt.Errorf("a fresh run directory is required: %w", err)
	}
	manifest := Manifest{Version: "review-run@v1", Task: req.Task, RunID: req.RunID, Actor: req.Actor, Policy: policy, PromptHash: digest(prompt), Command: req.Command, Created: time.Now().UTC()}
	result.ManifestHash, err = seal(filepath.Join(req.Dir, "manifest.json"), manifest)
	if err != nil {
		return result, err
	}
	if req.Registry != "" {
		if err = registerManifest(req.Registry, manifest, result.ManifestHash); err != nil {
			return result, err
		}
	}
	// Preserve the exact prompt, so later edits cannot alter original provenance.
	f, err := os.OpenFile(filepath.Join(req.Dir, "prompt.txt"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0400)
	if err != nil {
		return result, err
	}
	_, err = f.Write(prompt)
	closeErr := f.Close()
	if err != nil {
		return result, err
	}
	if closeErr != nil {
		return result, closeErr
	}
	out, err := os.OpenFile(filepath.Join(req.Dir, "stdout.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, err
	}
	defer out.Close()
	errout, err := os.OpenFile(filepath.Join(req.Dir, "stderr.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, err
	}
	defer errout.Close()
	gate, release, err := os.Pipe()
	if err != nil {
		return result, err
	}
	defer gate.Close()
	defer release.Close()
	args := append([]string{"-c", "read -r permit <&3 || exit 125; exec \"$@\"", "review-exec"}, req.Command...)
	program, args, cleanup, err := reviewIsolation(ctx, "/bin/sh", args)
	if err != nil {
		return result, err
	}
	defer cleanup()
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir = req.Repo
	cmd.Stdout = out
	cmd.Stderr = errout
	cmd.ExtraFiles = []*os.File{gate}
	cmd.Env = append(os.Environ(), "COMMANDER_REVIEW_RUN_DIR="+req.Dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { _ = cleanup(); return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	result.Started = time.Now().UTC()
	if err = cmd.Start(); err != nil {
		return result, err
	}
	result.PID = cmd.Process.Pid
	if _, err = seal(filepath.Join(req.Dir, "started.json"), result); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return result, err
	}
	_, err = release.Write([]byte("go\n"))
	_ = release.Close()
	if err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	waitErr := cmd.Wait()
	// Linux additionally owns the cgroup; other Unix hosts guarantee only this
	// process group and prohibit detached review work.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if cleanupErr := cleanup(); cleanupErr != nil && waitErr == nil {
		waitErr = cleanupErr
	}
	result.Finished = time.Now().UTC()
	result.Exit = cmd.ProcessState.ExitCode()
	result.Execution = "complete"
	if waitErr != nil || err != nil {
		result.Execution = "failed"
	}
	if ctx.Err() != nil {
		result.Execution = "blocked"
	}
	_ = out.Close()
	_ = errout.Close()
	result.StdoutHash, err = fileHash(filepath.Join(req.Dir, "stdout.log"))
	if err != nil {
		return result, err
	}
	result.StderrHash, err = fileHash(filepath.Join(req.Dir, "stderr.log"))
	if err != nil {
		return result, err
	}
	if _, err = seal(filepath.Join(req.Dir, "terminal.json"), result); err != nil {
		return result, err
	}
	if req.Registry != "" {
		if _, err = seal(filepath.Join(registryDir(req.Registry, req.RunID), result.ManifestHash+".terminal.json"), result); err != nil {
			return result, err
		}
	}
	_ = os.Chmod(filepath.Join(req.Dir, "stdout.log"), 0400)
	_ = os.Chmod(filepath.Join(req.Dir, "stderr.log"), 0400)
	return result, waitErr
}
