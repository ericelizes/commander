// Package binstage is the explicit install step for cmdr and cmdrd.
//
// Builds write only to <repo>/bin/staged (scripts/stage.sh). Install copies the
// staged binaries, atomically and as real files, into a directory outside the
// repo and points the ~/.local/bin links there, so no build can change what is
// running. The previous binaries stay as <name>.prev for Rollback.
package binstage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Binaries are the files staged and installed.
var Binaries = []string{"cmdr", "cmdrd"}

// Links maps each ~/.local/bin link to the installed binary it points at.
var Links = map[string]string{"cmdr": "cmdr", "commander": "cmdr", "cmdrd": "cmdrd"}

// InstallDir is where installed binaries live: $CMDR_INSTALL_DIR, else
// $COMMANDER_HOME/bin, else ~/.commander/bin.
func InstallDir(getenv func(string) string, home string) string {
	if d := getenv("CMDR_INSTALL_DIR"); d != "" {
		return d
	}
	if d := getenv("COMMANDER_HOME"); d != "" {
		return filepath.Join(d, "bin")
	}
	return filepath.Join(home, ".commander", "bin")
}

// Options configure Install and Rollback. LocalBin is the directory holding
// the cmdr/cmdrd/commander links (~/.local/bin).
type Options struct {
	StageDir   string // Install only
	InstallDir string
	LocalBin   string
	Repo       string // recorded beside the binaries so they find the checkout
	Out        io.Writer
}

// Revision reads the VERSION file in dir; "" when absent.
func Revision(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "VERSION"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// RepoHint returns the checkout recorded by Install in the directory holding
// exe (symlinks resolved), or "" when none is recorded or it no longer exists.
func RepoHint(exe string) string {
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(exe), "REPO"))
	if err != nil {
		return ""
	}
	repo := strings.TrimSpace(string(raw))
	if fi, err := os.Stat(repo); err != nil || !fi.IsDir() {
		return "" // checkout gone: callers fall back to the go.mod walk
	}
	return repo
}

// ValidateRepo checks that dir can be recorded as the checkout: a directory
// with go.mod that is not a linked git worktree (its .git is a file).
func ValidateRepo(dir string) error {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("repo %s is not a directory", dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return fmt.Errorf("repo %s has no go.mod", dir)
	}
	if fi, err := os.Lstat(filepath.Join(dir, ".git")); err == nil && !fi.IsDir() {
		return fmt.Errorf("repo %s is a git worktree; install from the main checkout", dir)
	}
	return nil
}

// Install copies the staged binaries into InstallDir, keeping the previous ones
// as .prev, then replaces the links in LocalBin. It never restarts anything.
// When the staged revision is already installed it copies nothing and leaves
// .prev alone, so a repeat run cannot lose the rollback target.
func Install(o Options) error {
	for _, name := range Binaries {
		if _, err := os.Stat(filepath.Join(o.StageDir, name)); err != nil {
			return fmt.Errorf("nothing staged: %s missing (run `just build`)", filepath.Join(o.StageDir, name))
		}
	}
	staged := Revision(o.StageDir)
	if staged == "" {
		return fmt.Errorf("nothing staged: %s has no VERSION (run `just build`)", o.StageDir)
	}
	if o.Repo != "" {
		if err := ValidateRepo(o.Repo); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(o.InstallDir, 0o755); err != nil {
		return err
	}
	current := Revision(o.InstallDir)
	if current == staged && installedComplete(o.InstallDir) {
		fmt.Fprintf(o.Out, "revision: %s already installed; nothing copied\n", staged)
		if err := writeRepo(o); err != nil {
			return err
		}
		return linkAll(o)
	}
	if current == "" {
		current = "none"
	}
	fmt.Fprintf(o.Out, "revision: %s -> %s\n", current, staged)

	// Stage every .new file first, so a failed copy changes nothing installed.
	files := append(append([]string{}, Binaries...), "VERSION")
	for _, name := range files {
		src, mode := filepath.Join(o.StageDir, name), os.FileMode(0o755)
		if name == "VERSION" {
			mode = 0o644
		}
		if err := copyFile(src, filepath.Join(o.InstallDir, name+".new"), mode); err != nil {
			removeNew(o.InstallDir, files)
			return err
		}
	}
	// Then swap them in; dest is never absent (.prev is a hardlink or copy of it).
	var done []string
	for _, name := range files {
		dest := filepath.Join(o.InstallDir, name)
		if err := keepPrev(dest); err != nil {
			rollbackDone(o.InstallDir, done)
			removeNew(o.InstallDir, files)
			return err
		}
		if err := os.Rename(dest+".new", dest); err != nil {
			rollbackDone(o.InstallDir, append(done, name))
			removeNew(o.InstallDir, files)
			return err
		}
		done = append(done, name)
		fmt.Fprintf(o.Out, "installed %s\n", dest)
	}
	if err := writeRepo(o); err != nil {
		return err
	}
	return linkAll(o)
}

func installedComplete(dir string) bool {
	for _, name := range Binaries {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return true
}

func writeRepo(o Options) error {
	if o.Repo == "" {
		return nil
	}
	return writeFile(filepath.Join(o.InstallDir, "REPO"), o.Repo)
}

// keepPrev makes dest.prev a copy of dest (a hardlink when possible) while dest
// stays in place; nothing to do when dest does not exist yet.
func keepPrev(dest string) error {
	if _, err := os.Lstat(dest); err != nil {
		return nil
	}
	tmp := dest + ".prev.new"
	_ = os.Remove(tmp)
	if err := os.Link(dest, tmp); err != nil {
		fi, serr := os.Stat(dest)
		if serr != nil {
			return serr
		}
		if err := copyFile(dest, tmp, fi.Mode().Perm()); err != nil {
			return err
		}
	}
	return os.Rename(tmp, dest+".prev")
}

// rollbackDone puts back the files already swapped from their .prev.
func rollbackDone(dir string, names []string) {
	for _, name := range names {
		dest := filepath.Join(dir, name)
		if _, err := os.Stat(dest + ".prev"); err == nil {
			_ = os.Rename(dest+".prev", dest)
		}
	}
}

func removeNew(dir string, names []string) {
	for _, name := range names {
		_ = os.Remove(filepath.Join(dir, name+".new"))
	}
}

// Rollback swaps each installed binary with its .prev (so a second rollback
// goes forward again) and the revision files with it.
func Rollback(o Options) error {
	for _, name := range Binaries {
		if _, err := os.Stat(filepath.Join(o.InstallDir, name+".prev")); err != nil {
			return fmt.Errorf("no previous %s in %s to roll back to", name, o.InstallDir)
		}
	}
	fmt.Fprintf(o.Out, "revision: %s -> %s\n", Revision(o.InstallDir), Revision2(o.InstallDir))
	for _, name := range Binaries {
		if err := swap(filepath.Join(o.InstallDir, name)); err != nil {
			return err
		}
		fmt.Fprintf(o.Out, "rolled back %s\n", filepath.Join(o.InstallDir, name))
	}
	if _, err := os.Stat(filepath.Join(o.InstallDir, "VERSION.prev")); err == nil {
		if err := swap(filepath.Join(o.InstallDir, "VERSION")); err != nil {
			return err
		}
	}
	return linkAll(o)
}

// Revision2 is the revision kept as VERSION.prev.
func Revision2(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "VERSION.prev"))
	if err != nil {
		return "none"
	}
	return strings.TrimSpace(string(raw))
}

// swap exchanges path and path.prev (VERSION swaps with VERSION.prev).
func swap(path string) error {
	prev, tmp := path+".prev", path+".swap"
	if err := os.Rename(path, tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(prev, path); err != nil {
		return err
	}
	if _, err := os.Lstat(tmp); err == nil {
		return os.Rename(tmp, prev)
	}
	return nil
}

func linkAll(o Options) error {
	if err := os.MkdirAll(o.LocalBin, 0o755); err != nil {
		return err
	}
	for link, target := range Links {
		path := filepath.Join(o.LocalBin, link)
		want := filepath.Join(o.InstallDir, target)
		if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink == 0 {
			fmt.Fprintf(o.Out, "warning: %s exists and is not a symlink; left alone\n", path)
			continue
		}
		tmp := path + ".new"
		_ = os.Remove(tmp)
		if err := os.Symlink(want, tmp); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
		fmt.Fprintf(o.Out, "link %s -> %s\n", path, want)
	}
	return nil
}

func writeFile(path, content string) error {
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(content+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}
