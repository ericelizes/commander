package spawn

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/docker"
	"github.com/example-user/commander/internal/opencode"
	"github.com/example-user/commander/internal/tmuxctl"
)

// newSpawnerForTest assembles a Spawner with mocks ready for happy-path and
// rollback exercise. Callers tweak fields after construction.
func newSpawnerForTest(t *testing.T, projectPath, capturedID string) (*Spawner, *fakeStore, *opencode.MockClient, *beads.MockClient, *agentmail.MockClient, *docker.MockClient) {
	t.Helper()
	fs := newFakeStore()
	oc := &opencode.MockClient{}
	bd := &beads.MockClient{}
	am := &agentmail.MockClient{}
	dk := &docker.MockClient{SpawnIDs: []string{"container-abc123"}}
	tx := &tmuxctl.MockClient{}

	commanderRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(commanderRepo, ".opencode", "agents"), 0o755); err != nil {
		t.Fatal(err)
	}

	sp := New(fs, oc, bd, am, dk, tx, commanderRepo)
	sp.useHost = func() bool { return false } // these tests exercise the Docker flow
	sp.shortID = func() string { return "deadbeef" }
	sp.captureSession = func(_ context.Context, _ string, _ time.Duration) (string, error) {
		return capturedID, nil
	}
	sp.now = func() time.Time { return time.Unix(1700000000, 0) }
	return sp, fs, oc, bd, am, dk
}

// goodRequest builds a SpawnRequest pointing at a real git repo.
func goodRequest(t *testing.T, projectPath string) SpawnRequest {
	t.Helper()
	return SpawnRequest{
		ProjectID:   42,
		ProjectName: "myproj",
		ProjectPath: projectPath,
		ProjectType: "code",
		TaskBeadID:  "bd-100",
		Branch:      "feat/auth",
		Model:       "anthropic/claude-sonnet-4-5",
	}
}

func TestSpawn_HappyPath(t *testing.T) {
	withTempHome(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := makeGitRepo(t)

	sp, fs, _, bd, am, dk := newSpawnerForTest(t, dir, "ses_capturedXYZ")

	res, err := sp.Spawn(context.Background(), goodRequest(t, dir))
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// SpawnResult shape.
	if res.SessionID != "ses_capturedXYZ" {
		t.Errorf("SessionID: got %q want ses_capturedXYZ", res.SessionID)
	}
	if res.ContainerID != "container-abc123" {
		t.Errorf("ContainerID: got %q want container-abc123", res.ContainerID)
	}
	if res.AgentName != "operator-feat-auth" {
		t.Errorf("AgentName: got %q want operator-feat-auth", res.AgentName)
	}
	wantWorktree := filepath.Join(dir, ".worktrees", "feat-auth")
	if res.WorktreePath != wantWorktree {
		t.Errorf("WorktreePath: got %q want %q", res.WorktreePath, wantWorktree)
	}
	if res.RunID == 0 {
		t.Error("RunID should be non-zero")
	}

	// Worktree exists on disk.
	if _, err := os.Stat(wantWorktree); err != nil {
		t.Errorf("worktree not created: %v", err)
	}

	// Session-id file persisted.
	sessionFile := filepath.Join(dir, ".commander", "operators", "feat-auth.session-id")
	body, err := os.ReadFile(sessionFile)
	if err != nil {
		t.Errorf("session file: %v", err)
	}
	if strings.TrimSpace(string(body)) != "ses_capturedXYZ" {
		t.Errorf("session file body: got %q", string(body))
	}

	// .git/info/exclude updated.
	excl, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(excl), ".commander/") || !strings.Contains(string(excl), ".worktrees/") {
		t.Errorf("exclude not updated: %s", string(excl))
	}

	// am.Register called with the right args.
	wantRegister := "Register:myproj/opencode-ai/sonnet/operator-feat-auth"
	found := false
	for _, c := range am.Calls {
		if c == wantRegister {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("am.Register not called with %q; calls=%v", wantRegister, am.Calls)
	}

	// bd.SetState called.
	if writes := beadMutations(bd.Calls); len(writes) != 1 || !strings.HasPrefix(writes[0], "SetState:bd-100:cmdr=brainstorming") {
		t.Errorf("beads.SetState: %v", bd.Calls)
	}

	// docker.Spawn called once with expected name.
	if len(dk.Calls) != 1 || !strings.Contains(dk.Calls[0], "name=cmdr-myproj-feat-auth-deadbeef") {
		t.Errorf("docker.Spawn: %v", dk.Calls)
	}

	// agent_runs row.
	if len(fs.runs) != 1 {
		t.Fatalf("agent_runs: got %d rows want 1", len(fs.runs))
	}
	r := fs.runs[0]
	if r.OpencodeSessionID != "ses_capturedXYZ" || !r.ContainerID.Valid || r.ContainerID.String != "container-abc123" {
		t.Errorf("agent_runs row: %+v", r)
	}
	if r.Status != "running" {
		t.Errorf("status: got %q want running", r.Status)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "opencode", "agents", "operator-feat-auth.md")); err != nil {
		t.Errorf("operator agent prompt missing: %v", err)
	}
}

func TestSpawn_CodeOperatorMountsProjectRootAtWorkspace(t *testing.T) {
	withTempHome(t)
	dir := makeGitRepo(t)

	sp, _, _, _, _, dk := newSpawnerForTest(t, dir, "ses_capturedXYZ")
	var captured docker.SpawnOptions
	dk.SpawnFunc = func(ctx context.Context, opts docker.SpawnOptions) (string, error) {
		captured = opts
		return "container-abc123", nil
	}

	if _, err := sp.Spawn(context.Background(), goodRequest(t, dir)); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	foundWorkspaceMount := false
	foundWorktreeMount := false
	for _, v := range captured.Volumes {
		if v.Source == dir && v.Target == "/workspace" && !v.ReadOnly {
			foundWorkspaceMount = true
		}
		if v.Target == "/worktree" && !v.ReadOnly {
			foundWorktreeMount = true
		}
	}
	if !foundWorkspaceMount {
		t.Fatalf("expected project root %q to be mounted at /workspace; got %+v", dir, captured.Volumes)
	}
	if !foundWorktreeMount {
		t.Fatalf("expected worktree mount at /worktree; got %+v", captured.Volumes)
	}
}

func TestSpawn_Rollback_DockerSpawnFails(t *testing.T) {
	withTempHome(t)
	dir := makeGitRepo(t)

	sp, fs, _, bd, am, dk := newSpawnerForTest(t, dir, "ses_x")
	dk.SpawnIDs = nil
	dk.SpawnErr = errors.New("docker daemon down")

	res, err := sp.Spawn(context.Background(), goodRequest(t, dir))
	if err == nil {
		t.Fatalf("expected error, got result %+v", res)
	}

	// Worktree must NOT exist after rollback.
	worktree := filepath.Join(dir, ".worktrees", "feat-auth")
	if _, err := os.Stat(worktree); err == nil {
		t.Errorf("worktree should be removed after rollback")
	}

	// No agent_runs row.
	if len(fs.runs) != 0 {
		t.Errorf("agent_runs should be empty, got %v", fs.runs)
	}

	// am.Register and bd.SetState should NOT have run (they come after docker).
	for _, c := range am.Calls {
		if strings.HasPrefix(c, "Register:") {
			t.Errorf("am.Register should not have run: %v", am.Calls)
		}
	}
	if len(bd.Calls) != 0 {
		t.Errorf("bd.SetState should not have run: %v", bd.Calls)
	}

	// Lock should have been released so a re-spawn succeeds.
	dk.SpawnErr = nil
	dk.SpawnIDs = []string{"container-retry"}
	if _, err := sp.Spawn(context.Background(), goodRequest(t, dir)); err != nil {
		t.Errorf("re-spawn after rollback failed: %v", err)
	}
}

func TestSpawn_Rollback_CaptureTimeout(t *testing.T) {
	withTempHome(t)
	dir := makeGitRepo(t)

	sp, fs, _, _, _, dk := newSpawnerForTest(t, dir, "")
	sp.captureSession = func(_ context.Context, _ string, _ time.Duration) (string, error) {
		return "", errors.New("session-capture timeout after 30s")
	}

	_, err := sp.Spawn(context.Background(), goodRequest(t, dir))
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "capture session") {
		t.Errorf("error wording: %v", err)
	}

	// Container must have been stopped.
	stopped := false
	for _, c := range dk.Calls {
		if strings.HasPrefix(c, "Stop(id=") {
			stopped = true
		}
	}
	if !stopped {
		t.Errorf("docker.Stop should have been called: %v", dk.Calls)
	}

	// Session-id file MUST NOT exist (capture failed before write).
	if _, err := os.Stat(filepath.Join(dir, ".commander", "operators", "feat-auth.session-id")); err == nil {
		t.Errorf("session-id file should not exist after rollback")
	}

	// No agent_runs row.
	if len(fs.runs) != 0 {
		t.Errorf("agent_runs should be empty, got %v", fs.runs)
	}
}

func TestSpawn_Rollback_AmRegisterFails(t *testing.T) {
	withTempHome(t)
	dir := makeGitRepo(t)

	sp, fs, _, _, am, dk := newSpawnerForTest(t, dir, "ses_x")
	am.RegisterErr = errors.New("am unreachable")

	_, err := sp.Spawn(context.Background(), goodRequest(t, dir))
	if err == nil {
		t.Fatal("expected error")
	}

	// Container stopped.
	stopped := false
	for _, c := range dk.Calls {
		if strings.HasPrefix(c, "Stop(id=") {
			stopped = true
		}
	}
	if !stopped {
		t.Errorf("docker.Stop should have been called: %v", dk.Calls)
	}

	// Session-id file removed.
	if _, err := os.Stat(filepath.Join(dir, ".commander", "operators", "feat-auth.session-id")); err == nil {
		t.Error("session-id file should be removed")
	}

	// No agent_runs row.
	if len(fs.runs) != 0 {
		t.Errorf("agent_runs should be empty, got %v", fs.runs)
	}
}

func TestSpawn_Validation_RejectsBadBranch(t *testing.T) {
	withTempHome(t)
	dir := makeGitRepo(t)
	sp, _, _, _, _, _ := newSpawnerForTest(t, dir, "ses_x")

	r := goodRequest(t, dir)
	r.Branch = "feat;rm -rf /"
	if _, err := sp.Spawn(context.Background(), r); err == nil {
		t.Error("expected validation error on shell-injected branch")
	}
}

func TestSpawn_WorktreeIdempotent_BForcesUpdate(t *testing.T) {
	withTempHome(t)
	dir := makeGitRepo(t)

	// Pre-create the branch so -B has something to override.
	mustRun(t, dir, "git", "branch", "feat/auth")

	sp, _, _, _, _, _ := newSpawnerForTest(t, dir, "ses_x")
	if _, err := sp.Spawn(context.Background(), goodRequest(t, dir)); err != nil {
		t.Errorf("Spawn should succeed when branch already exists: %v", err)
	}
}

// TestSpawn_CodeMountsReadsAppendsROVolumes asserts that req.MountReads paths
// are passed to docker.Spawn as additional :ro volume mounts targeting
// /mounts/<basename>. U19.
func TestSpawn_CodeMountsReadsAppendsROVolumes(t *testing.T) {
	withTempHome(t)
	dir := makeGitRepo(t)

	// Create two real read-only candidate dirs with distinct basenames.
	mountA := filepath.Join(t.TempDir(), "frontend")
	mountB := filepath.Join(t.TempDir(), "sandbox")
	if err := os.MkdirAll(mountA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(mountB, 0o755); err != nil {
		t.Fatal(err)
	}

	sp, _, _, _, _, dk := newSpawnerForTest(t, dir, "ses_x")
	// Capture the SpawnOptions handed to the docker client so we can inspect
	// the volume list directly (MockClient.record only stringifies image+name).
	var got docker.SpawnOptions
	dk.SpawnFunc = func(_ context.Context, opts docker.SpawnOptions) (string, error) {
		got = opts
		return "container-mounts", nil
	}

	req := goodRequest(t, dir)
	req.MountReads = []string{mountA, mountB}

	if _, err := sp.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Both mounts must appear with the expected target + :ro flag.
	hasA, hasB := false, false
	for _, v := range got.Volumes {
		if v.Source == mountA && v.Target == "/mounts/frontend" && v.ReadOnly {
			hasA = true
		}
		if v.Source == mountB && v.Target == "/mounts/sandbox" && v.ReadOnly {
			hasB = true
		}
	}
	if !hasA {
		t.Errorf("missing read-only volume for %s -> /mounts/frontend; got %+v", mountA, got.Volumes)
	}
	if !hasB {
		t.Errorf("missing read-only volume for %s -> /mounts/sandbox; got %+v", mountB, got.Volumes)
	}
}

// TestSpawn_CodeMountsReadsValidationFailsForNonDir asserts that a missing or
// non-directory MountReads entry fails validation BEFORE docker run. U19.
func TestSpawn_CodeMountsReadsValidationFailsForNonDir(t *testing.T) {
	withTempHome(t)
	dir := makeGitRepo(t)

	sp, fs, _, _, _, dk := newSpawnerForTest(t, dir, "ses_x")

	req := goodRequest(t, dir)
	req.MountReads = []string{filepath.Join(t.TempDir(), "does-not-exist")}

	if _, err := sp.Spawn(context.Background(), req); err == nil {
		t.Fatal("expected validation error for missing MountReads path")
	}

	// docker.Spawn must not have been invoked.
	for _, c := range dk.Calls {
		if strings.HasPrefix(c, "Spawn(") {
			t.Errorf("docker.Spawn must not run on validation failure; calls=%v", dk.Calls)
		}
	}
	if len(fs.runs) != 0 {
		t.Errorf("agent_runs should be empty, got %v", fs.runs)
	}

	// Also reject a path that exists but is a regular file (not a directory).
	regular := filepath.Join(t.TempDir(), "readme.txt")
	if err := os.WriteFile(regular, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	req.MountReads = []string{regular}
	if _, err := sp.Spawn(context.Background(), req); err == nil {
		t.Error("expected validation error for non-directory MountReads path")
	}
}

// The role slug comes from the bead-derived Name; the branch only names the git branch.
func TestSpawnRequestSlug(t *testing.T) {
	if got := (SpawnRequest{Branch: "feat/auth"}).slug(); got != "feat-auth" {
		t.Fatalf("branch fallback = %q", got)
	}
	if got := (SpawnRequest{Branch: "feat/auth", Name: "op-fix-bd-1"}).slug(); got != "op-fix-bd-1" {
		t.Fatalf("name = %q", got)
	}
}

func TestOperatorRole_CanonicalMatchesEitherSpelling(t *testing.T) {
	if CanonicalRole("operator-op-x-t02.6.2.18") != CanonicalRole("operator-op-x-t02-6-2-18") {
		t.Fatal("either spelling must give the same role")
	}
	if got := CanonicalRole("operator-x.1"); got != "operator-x-1" {
		t.Fatalf("got %q", got)
	}
}

func TestAppendGitExcludes_Worktrees(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte("# x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := appendGitExcludes(dir); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".commander/", ".worktrees", ".worktrees/", ".worktrees.noindex/"} {
		if c := strings.Count("\n"+string(body), "\n"+want+"\n"); c != 1 {
			t.Errorf("%q appeared %d times, want 1", want, c)
		}
	}
}
