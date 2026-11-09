// Package worktreelock freezes an Operator's worktree once its run has closed,
// so nothing keeps writing to it (a Manager "helping", a stray process). Lock is
// `git worktree lock` plus a-w on every file; Unlock undoes both before a
// reopened bead's Operator is spawned again. Prune deletes one only when its
// bead is closed and nothing in it is missing from main.
package worktreelock

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
)

const lockReason = "operator run closed; commander unlocks it on respawn"

// Lock write-protects dir and locks it in git. Missing dirs are a no-op.
func Lock(dir string) error {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil
	}
	// Already-locked is fine, so the git error is ignored.
	_ = exec.Command("git", "-C", dir, "worktree", "lock", "--reason", lockReason, dir).Run()
	return chmodTree(dir, func(m fs.FileMode) fs.FileMode { return m &^ 0o222 })
}

// Unlock makes dir writable for its owner again and unlocks it in git.
func Unlock(dir string) error {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil
	}
	err := chmodTree(dir, func(m fs.FileMode) fs.FileMode { return m | 0o200 })
	_ = exec.Command("git", "-C", dir, "worktree", "unlock", dir).Run()
	return err
}

// chmodTree applies change to every non-symlink entry under root, the root
// directory last when removing write bits so the walk can still enter it.
func chmodTree(root string, change func(fs.FileMode) fs.FileMode) error {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if d.IsDir() {
			// Writable first so Unlock can enter and fix children.
			_ = os.Chmod(path, info.Mode().Perm()|0o200)
			dirs = append(dirs, path)
			return nil
		}
		return os.Chmod(path, change(info.Mode().Perm()))
	})
	for i := len(dirs) - 1; i >= 0; i-- { // children before parents
		if info, statErr := os.Stat(dirs[i]); statErr == nil {
			if cerr := os.Chmod(dirs[i], change(info.Mode().Perm())); cerr != nil && err == nil {
				err = cerr
			}
		}
	}
	return err
}
