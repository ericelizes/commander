package spawn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/worktreelock"
)

// A closed run's settle freezes its worktree, possibly long after the sweep (its
// retries last 30 minutes), when a new Operator may be working there. So the
// freeze itself refuses a worktree a running run owns, and a spawn holds a claim
// from before it clears the lock until its row names the worktree: whichever gets
// the claim first, the freeze never lands after the spawn cleared the lock. A claim
// is a flock, so a crash lets it go and nothing can be held forever.

// worktreeClaimWait bounds a spawn's wait for a freeze in progress (a chmod walk).
const worktreeClaimWait = 2 * time.Minute

// RunWorktree is the worktree run works in. A follow-up Operator that took over
// another's has it on its row; without one it is the run's own name's.
func RunWorktree(projectPath string, run store.AgentRun) string {
	if run.WorktreePath.Valid && run.WorktreePath.String != "" {
		return run.WorktreePath.String
	}
	return filepath.Join(projectPath, ".worktrees", strings.TrimPrefix(run.AgentName, "operator-"))
}

// FreezeWorktree locks closed's worktree unless it is no Operator worktree (the
// lock is a recursive chmod; a row naming the main checkout must not reach every
// worktree under it), another run owns it or a spawn is taking it over. A read it
// cannot make freezes nothing: an unfrozen worktree is a lost safeguard, a frozen
// one under a working Operator breaks its run.
func FreezeWorktree(ctx context.Context, running func(context.Context) ([]store.AgentRun, error), lock func(string) error, projectPath string, closed store.AgentRun) error {
	dir := RunWorktree(projectPath, closed)
	if !isOperatorWorktree(projectPath, dir) {
		log.Printf("spawn: %s not frozen: not a direct child of the project's worktree folders", dir)
		return nil
	}
	release, held, err := tryClaimWorktree(dir)
	if err != nil {
		return fmt.Errorf("%s not frozen: %w", dir, err)
	}
	if !held {
		log.Printf("spawn: %s not frozen: a spawn is taking it over", dir)
		return nil
	}
	defer release()
	runs, err := running(ctx)
	if err != nil {
		return fmt.Errorf("%s not frozen: read running runs: %w", dir, err)
	}
	target := resolvedPath(dir)
	for _, r := range runs {
		if r.ID != closed.ID && r.ProjectID == closed.ProjectID && resolvedPath(RunWorktree(projectPath, r)) == target {
			log.Printf("spawn: %s not frozen: %s works there", dir, r.AgentName)
			return nil
		}
	}
	return lock(dir)
}

// isOperatorWorktree reports whether dir is a direct child of
// <project>/.worktrees or .worktrees.noindex, comparing real paths.
func isOperatorWorktree(projectPath, dir string) bool {
	parent := filepath.Dir(resolvedPath(dir))
	for _, name := range worktreelock.WorktreeDirNames {
		if parent == resolvedPath(filepath.Join(projectPath, name)) {
			return true
		}
	}
	return false
}

// claimWorktree waits for dir's claim; a spawn cannot go on while a freeze runs.
func claimWorktree(ctx context.Context, dir string) (func(), error) {
	deadline := time.Now().Add(worktreeClaimWait)
	for {
		release, held, err := tryClaimWorktree(dir)
		if err != nil {
			return nil, err
		}
		if held {
			return release, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("worktree %s is still claimed after %s (a freeze or another spawn): retry", dir, worktreeClaimWait)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("worktree %s is claimed (a freeze or another spawn): %w", dir, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// tryClaimWorktree takes dir's claim if it is free. Paths are real paths, as
// .worktrees may link to .worktrees.noindex.
func tryClaimWorktree(dir string) (release func(), held bool, err error) {
	claims := filepath.Join(agentrt.CommanderHome(), "worktree-claims")
	if err := os.MkdirAll(claims, 0o755); err != nil {
		return nil, false, err
	}
	sum := sha256.Sum256([]byte(resolvedPath(dir)))
	f, err := os.OpenFile(filepath.Join(claims, hex.EncodeToString(sum[:8])+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() { _ = f.Close() }, true, nil
}

// resolvedPath is dir with links resolved, also when dir itself does not exist
// yet (a new worktree under a linked .worktrees).
func resolvedPath(dir string) string {
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		return r
	}
	if r, err := filepath.EvalSymlinks(filepath.Dir(dir)); err == nil {
		return filepath.Join(r, filepath.Base(dir))
	}
	return filepath.Clean(dir)
}
