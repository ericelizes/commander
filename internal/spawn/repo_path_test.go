package spawn

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A knowledge project whose path is a plain directory (no .git) spawns a host
// claude with cwd = the project path and no worktree.
func TestSpawnKnowledgeHost_NoGitNeeded(t *testing.T) {
	dir := t.TempDir()
	sp, fs, _, _, dk, tx := newHostSpawner(t, dir)

	req := goodRequest(t, dir)
	req.ProjectType = "knowledge"
	req.Branch = ""
	res, err := sp.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if res.WorktreePath != "" || res.AgentName != "operator-100" || res.SessionID == "" {
		t.Errorf("result: %+v", res)
	}
	if len(dk.Calls) != 0 {
		t.Errorf("no docker: %v", dk.Calls)
	}
	if _, err := os.Stat(filepath.Join(dir, ".worktrees")); err == nil {
		t.Error("knowledge spawn must not make a worktree")
	}
	if len(tx.Calls) != 3 || !strings.Contains(tx.Calls[0], "cwd="+dir+" ") || !strings.Contains(tx.Calls[2], "RespawnPane(target=cmdr-myproj-100 cwd="+dir+" ") {
		t.Fatalf("tmux calls: %v", tx.Calls)
	}
	body, err := os.ReadFile(filepath.Join(dir, ".commander", "operators", "operator-100.sh"))
	if err != nil || !strings.Contains(string(body), "knowledge Operator") || !strings.Contains(string(body), "'claude' '-p'") {
		t.Errorf("launcher: %s %v", body, err)
	}
	if len(fs.runs) != 1 || !fs.runs[0].TmuxPaneID.Valid {
		t.Errorf("runs: %+v", fs.runs)
	}
}

// RepoPath moves the git side (worktree source) while beads/.commander stay in
// the project path.
func TestSpawnCodeHost_RepoPathSuppliesWorktree(t *testing.T) {
	project := t.TempDir() // no .git: the beads root
	repo := makeGitRepo(t)
	sp, _, _, _, _, _ := newHostSpawner(t, project)

	req := goodRequest(t, project)
	req.RepoPath = repo
	res, err := sp.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if want := filepath.Join(project, ".worktrees", "feat-auth"); res.WorktreePath != want {
		t.Errorf("worktree %q, want %q", res.WorktreePath, want)
	}
	if _, err := os.Stat(filepath.Join(res.WorktreePath, "README")); err != nil {
		t.Errorf("worktree is not a checkout of the repo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, ".commander", "operators")); err != nil {
		t.Errorf("operators dir belongs in the project path: %v", err)
	}
}

func TestSpawnCode_NotAGitRepoNamesTheFix(t *testing.T) {
	project := t.TempDir()
	sp, _, _, _, _, _ := newHostSpawner(t, project)

	_, err := sp.Spawn(context.Background(), goodRequest(t, project))
	if err == nil || !strings.Contains(err.Error(), "cmdr project set-repo myproj") {
		t.Fatalf("want an error naming `cmdr project set-repo`, got %v", err)
	}

	req := goodRequest(t, project)
	req.RepoPath = filepath.Join(project, "repo")
	_ = os.MkdirAll(req.RepoPath, 0o755)
	_, err = sp.Spawn(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "RepoPath") || !strings.Contains(err.Error(), "set-repo") {
		t.Fatalf("bad RepoPath error: %v", err)
	}
}
