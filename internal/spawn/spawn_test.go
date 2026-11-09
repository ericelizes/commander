package spawn

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/example-user/commander/internal/store"
)

// fakeStore is a minimal AgentRunStore used by spawn_test.go and the rollback
// + reaper suites. Records every call and supports scripted errors.
type fakeStore struct {
	mu sync.Mutex

	runs       []store.AgentRun
	costEvents []store.CostEvent
	closed     map[int64]string

	insertAgentRunErr  error
	insertCostEventErr error
	getAgentRunResult  *store.AgentRun
	getAgentRunErr     error
	markClosedErr      error
	nextID             int64
}

func newFakeStore() *fakeStore {
	return &fakeStore{closed: map[int64]string{}}
}

func (f *fakeStore) InsertAgentRun(_ context.Context, r store.AgentRun) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.insertAgentRunErr != nil {
		return 0, f.insertAgentRunErr
	}
	f.nextID++
	r.ID = f.nextID
	f.runs = append(f.runs, r)
	return r.ID, nil
}

func (f *fakeStore) GetAgentRunByContainer(_ context.Context, containerID string) (*store.AgentRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getAgentRunErr != nil {
		return nil, f.getAgentRunErr
	}
	if f.getAgentRunResult != nil {
		// Stub mode: return the pre-canned result regardless of input.
		return f.getAgentRunResult, nil
	}
	for i := range f.runs {
		if f.runs[i].ContainerID.Valid && f.runs[i].ContainerID.String == containerID {
			cp := f.runs[i]
			return &cp, nil
		}
	}
	return nil, nil
}

func (f *fakeStore) MarkClosed(_ context.Context, runID int64, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.markClosedErr != nil {
		return f.markClosedErr
	}
	f.closed[runID] = status
	return nil
}

func (f *fakeStore) ListRunningRuns(context.Context) ([]store.AgentRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.AgentRun
	for _, r := range f.runs {
		if _, closed := f.closed[r.ID]; !closed && r.Status == "running" {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeStore) InsertCostEvent(_ context.Context, e store.CostEvent) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.insertCostEventErr != nil {
		return 0, f.insertCostEventErr
	}
	f.costEvents = append(f.costEvents, e)
	return int64(len(f.costEvents)), nil
}

// makeGitDir builds a t.TempDir-rooted project with a .git/info/exclude file
// so validateRequest's code-type checks pass.
func makeGitDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git", "info"), 0o755); err != nil {
		t.Fatalf("mkdir .git/info: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte("# initial\n"), 0o644); err != nil {
		t.Fatalf("write exclude: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".commander"), 0o755); err != nil {
		t.Fatalf("mkdir .commander: %v", err)
	}
	return dir
}

// withTempHome reroutes $HOME so spawn-lock files and ~/.commander land in
// t.TempDir, isolating tests from each other.
func withTempHome(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
}

// makeGitRepo upgrades makeGitDir's mocked .git/ into a real git repo so
// gitWorktreeAdd actually works. Returns the same path.
func makeGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := os.Stat("/usr/bin/git"); err != nil {
		if _, err2 := os.Stat("/opt/homebrew/bin/git"); err2 != nil {
			t.Skip("git binary not found; skipping integration-flavored test")
		}
	}
	dir := t.TempDir()
	mustRun(t, dir, "git", "init", "-b", "main")
	mustRun(t, dir, "git", "config", "user.email", "t@example.com")
	mustRun(t, dir, "git", "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "git", "add", "README")
	mustRun(t, dir, "git", "commit", "-m", "init")
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".commander"), 0o755); err != nil {
		t.Fatalf("mkdir .commander: %v", err)
	}
	return dir
}

func mustRun(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v: %s", name, strings.Join(args, " "), err, string(out))
	}
}

// TestFlatBranch covers the slug helper used to derive operator agent names
// and worktree paths.
func TestFlatBranch(t *testing.T) {
	cases := map[string]string{
		"feat/auth":   "feat-auth",
		"x/y/z":       "x-y-z",
		"main":        "main",
		"release/1.2": "release-1.2",
		"refactor_x":  "refactor_x",
	}
	for in, want := range cases {
		if got := flatBranch(in); got != want {
			t.Errorf("flatBranch(%q): got %q want %q", in, got, want)
		}
	}
}

// TestModelShort covers the provider/model → short label helper used for am
// register's --model flag.
func TestModelShort(t *testing.T) {
	cases := map[string]string{
		"anthropic/claude-sonnet-4-5": "sonnet",
		"anthropic/claude-opus-4-7":   "opus",
		"anthropic/claude-haiku-3-5":  "haiku",
		"anthropic/some-future-model": "some-future-model",
		"sonnet":                      "sonnet",
	}
	for in, want := range cases {
		if got := modelShort(in); got != want {
			t.Errorf("modelShort(%q): got %q want %q", in, got, want)
		}
	}
}

// TestValidateRequest covers happy path + each rejection branch.
func TestValidateRequest(t *testing.T) {
	dir := makeGitDir(t)
	good := SpawnRequest{
		ProjectID:   1,
		ProjectName: "myproj",
		ProjectPath: dir,
		ProjectType: "code",
		TaskBeadID:  "bd-1",
		Branch:      "feat/auth",
		Model:       "anthropic/claude-sonnet-4-5",
	}
	if err := validateRequest(good); err != nil {
		t.Fatalf("good request rejected: %v", err)
	}

	bad := func(mut func(*SpawnRequest)) error {
		r := good
		mut(&r)
		return validateRequest(r)
	}
	type badCase struct {
		name string
		mut  func(*SpawnRequest)
	}
	for _, c := range []badCase{
		{"bad type", func(r *SpawnRequest) { r.ProjectType = "knowledge" }},
		{"empty branch", func(r *SpawnRequest) { r.Branch = "" }},
		{"branch with space", func(r *SpawnRequest) { r.Branch = "feat auth" }},
		{"branch with semicolon", func(r *SpawnRequest) { r.Branch = "feat;rm" }},
		{"empty model", func(r *SpawnRequest) { r.Model = "" }},
		{"empty task", func(r *SpawnRequest) { r.TaskBeadID = "" }},
		{"empty name", func(r *SpawnRequest) { r.ProjectName = "" }},
		{"relative path", func(r *SpawnRequest) { r.ProjectPath = "relative/path" }},
	} {
		if err := bad(c.mut); err == nil {
			t.Errorf("%s: expected error", c.name)
		}
	}

	// Missing .git → rejected.
	noGit := t.TempDir()
	r := good
	r.ProjectPath = noGit
	if err := validateRequest(r); err == nil {
		t.Error("missing .git: expected error")
	}
}

// TestAcquireSpawnLock_Mutex confirms two concurrent acquires for the same
// (project, branch) collide; the second one errors.
func TestAcquireSpawnLock_Mutex(t *testing.T) {
	withTempHome(t)
	first, err := acquireSpawnLock("p", "branch")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer first.release()

	if _, err := acquireSpawnLock("p", "branch"); err == nil {
		t.Fatal("expected second acquire to fail")
	}

	// Releasing the first lets a fresh acquire succeed.
	if err := first.release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	third, err := acquireSpawnLock("p", "branch")
	if err != nil {
		t.Fatalf("third acquire after release: %v", err)
	}
	_ = third.release()
}

// TestCleanupsRunReverse confirms cleanups fire LIFO and aggregate errors.
func TestCleanupsRunReverse(t *testing.T) {
	var order []int
	var c cleanups
	c.push(func() error { order = append(order, 1); return nil })
	c.push(func() error { order = append(order, 2); return errors.New("two failed") })
	c.push(func() error { order = append(order, 3); return nil })

	errs := c.runReverse(context.Background())
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(errs), errs)
	}
	if !errors.Is(errs[0], errs[0]) || errs[0].Error() != "two failed" {
		t.Errorf("error: %v", errs[0])
	}
	wantOrder := []int{3, 2, 1}
	for i, v := range wantOrder {
		if order[i] != v {
			t.Errorf("order[%d]: got %d want %d (full %v)", i, order[i], v, order)
		}
	}
}

// TestScanForSessionID covers the nd-JSON capture path used by docker-logs
// session-capture.
func TestScanForSessionID(t *testing.T) {
	stream := strings.NewReader(`
banner non-json
{"type":"step_start","timestamp":1,"sessionID":"ses_abc123","part":{}}
{"type":"text","timestamp":2,"sessionID":"ses_abc123","part":{"text":"hi"}}
`)
	id, err := scanForSessionID(stream)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if id != "ses_abc123" {
		t.Errorf("id: got %q want ses_abc123", id)
	}
}

// TestScanForSessionID_NoSessionID returns empty string when nothing matches.
func TestScanForSessionID_NoSessionID(t *testing.T) {
	stream := strings.NewReader(`{"type":"text","part":{"text":"no id here"}}`)
	id, err := scanForSessionID(stream)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if id != "" {
		t.Errorf("expected empty id, got %q", id)
	}
}
