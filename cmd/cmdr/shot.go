package main

import (
	"fmt"
	"github.com/example-user/commander/internal/binstage"
	"os"
	"os/exec"
	"path/filepath"
)

// runShot is `cmdr __shot [out.png] [window]`: render what the cockpit looks
// like (a read-only, size-ignoring tmux client in a pty, drawn to a PNG with
// pyte + Pillow). It needs no screen-recording permission and never changes the
// user's window. The Python deps live in a venv created on first use.
func runShot(args []string, stdout, stderr *os.File) int {
	home := appCommanderHome()
	venv := filepath.Join(home, "tools", "venv")
	py := filepath.Join(venv, "bin", "python")
	if _, err := os.Stat(py); err != nil {
		fmt.Fprintln(stderr, "cmdr __shot: creating the Python venv (pyte, pillow)…")
		for _, c := range [][]string{
			{"python3", "-m", "venv", venv},
			{py, "-m", "pip", "install", "-q", "pyte", "pillow"},
		} {
			cmd := exec.Command(c[0], c[1:]...)
			cmd.Stderr = stderr
			if err := cmd.Run(); err != nil {
				fmt.Fprintf(stderr, "cmdr __shot: %v\n", err)
				return 1
			}
		}
	}
	script := os.Getenv("COMMANDER_SHOT_SCRIPT")
	if script == "" {
		repo := resolveCommanderRepoForShot()
		script = filepath.Join(repo, "scripts", "shot.py")
	}
	cmd := exec.Command(py, append([]string{script}, args...)...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(stderr, "cmdr __shot: %v\n", err)
		return 1
	}
	return 0
}

func resolveCommanderRepoForShot() string {
	if exe, err := os.Executable(); err == nil {
		if repo := binstage.RepoHint(exe); repo != "" {
			return repo
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			// <repo>/bin/cmdr
			return filepath.Dir(filepath.Dir(resolved))
		}
	}
	return "."
}
