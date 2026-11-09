package activation

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/example-user/commander/internal/testguard"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/opencode"
	"github.com/example-user/commander/internal/store"
)

// TestMain defaults every test to a mock beads client so Activate never shells
// out to a real `br`. Tests that assert on beads calls override via useMockBeads.
func TestMain(m *testing.M) {
	newBeadsClientFn = func() beads.Client { return &beads.MockClient{} }
	// Pin the runtime so a developer's ~/.commander/config.toml can't flip
	// these opencode-shaped tests; Claude tests opt in with t.Setenv.
	os.Setenv(agentrt.EnvVar, string(agentrt.Opencode))
	cleanup := testguard.Isolate()
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// useMockBeads swaps newBeadsClientFn for a fresh mock so a test can assert on
// the beads calls Activate made. Returns the mock.
func useMockBeads(t *testing.T) *beads.MockClient {
	t.Helper()
	mb := &beads.MockClient{}
	orig := newBeadsClientFn
	newBeadsClientFn = func() beads.Client { return mb }
	t.Cleanup(func() { newBeadsClientFn = orig })
	return mb
}

// fakeStore is a minimal ProjectStore impl that mimics SQLite's UNIQUE-name
// behavior without touching SQLite.
type fakeStore struct {
	mu        sync.Mutex
	byName    map[string]*store.Project
	amAgents  map[string]string // "projectKey\x00role" -> am name
	insertID  int64
	insertErr error
	getErr    error
}

func newFakeStore() *fakeStore {
	return &fakeStore{byName: map[string]*store.Project{}, amAgents: map[string]string{}}
}

func (f *fakeStore) UpsertAmAgent(_ context.Context, amProjectKey, role, amName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.amAgents[amProjectKey+"\x00"+role] = amName
	return nil
}

func (f *fakeStore) GetAmAgent(_ context.Context, amProjectKey, role string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.amAgents[amProjectKey+"\x00"+role], nil
}

func (f *fakeStore) InsertProject(_ context.Context, p store.Project) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.insertErr != nil {
		return 0, f.insertErr
	}
	if _, dup := f.byName[p.Name]; dup {
		// Mirror SQLite's UNIQUE error string so the activation code's
		// substring check fires.
		return 0, errors.New("UNIQUE constraint failed: projects.name")
	}
	f.insertID++
	p.ID = f.insertID
	cp := p
	f.byName[p.Name] = &cp
	return p.ID, nil
}

func (f *fakeStore) GetProjectByName(_ context.Context, name string) (*store.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	p, ok := f.byName[name]
	if !ok {
		return nil, nil
	}
	return p, nil
}

// makeGitDir builds a t.TempDir-rooted project with a .git/info/exclude file
// so validateRequest's code-type checks pass and appendGitExcludes has work
// to do. Returns the absolute path.
func makeGitDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git", "info"), 0o755); err != nil {
		t.Fatalf("mkdir .git/info: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte("# git ls-files --exclude-from=.git/info/exclude\n"), 0o644); err != nil {
		t.Fatalf("write exclude: %v", err)
	}
	return dir
}

func TestActivate_HappyPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := makeGitDir(t)
	fs := newFakeStore()
	oc := &opencode.MockClient{}
	reg := &agentmail.MockRegistrar{NextName: "GoldTiger"}
	mb := useMockBeads(t)
	a := New(fs, oc, reg, "/tmp/example/code/commander")
	origCapture := captureManagerSessionFn
	origNormalize := normalizeManagerSessionFn
	captureManagerSessionFn = func(context.Context, ActivateRequest, string) (string, error) {
		return "ses_TESTID0123456789ABCDEFGHIJ", nil
	}
	normalizeManagerSessionFn = func(_ context.Context, _ ActivateRequest, sessionID string) (string, bool, error) {
		return sessionID, false, nil
	}
	t.Cleanup(func() {
		captureManagerSessionFn = origCapture
		normalizeManagerSessionFn = origNormalize
	})

	res, err := a.Activate(context.Background(), ActivateRequest{
		Name: "frontend", Type: "code", Path: dir,
	})
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if res.ProjectID != 1 {
		t.Errorf("ProjectID = %d, want 1", res.ProjectID)
	}
	if res.LeadSessionID != "ses_TESTID0123456789ABCDEFGHIJ" {
		t.Errorf("LeadSessionID = %q, want captured id", res.LeadSessionID)
	}
	if res.AmAgentName != "manager-frontend" {
		t.Errorf("AmAgentName = %q, want manager-frontend", res.AmAgentName)
	}
	if res.AlreadyActive {
		t.Error("AlreadyActive = true, want false on fresh activation")
	}

	// session-id file was persisted.
	body, err := os.ReadFile(res.LeadSessionIDPath)
	if err != nil {
		t.Fatalf("read session id file: %v", err)
	}
	if got := strings.TrimSpace(string(body)); got != res.LeadSessionID {
		t.Errorf("file body = %q, want %q", got, res.LeadSessionID)
	}

	// beads workspace was initialized for the project (so tasks can be created).
	if !strings.Contains(strings.Join(mb.Calls, "\n"), "Init:dir="+dir) {
		t.Errorf("beads Init not called for %s; calls=%v", dir, mb.Calls)
	}

	// One server registration (the server assigns the name), program opencode-ai.
	if len(reg.RegisterCalls) != 1 {
		t.Fatalf("RegisterCalls = %d, want 1 (server assigns one name)", len(reg.RegisterCalls))
	}
	if rc := reg.RegisterCalls[0]; rc.Program != "opencode-ai" || rc.ProjectKey != "/tmp/example/code/commander" {
		t.Errorf("RegisterAgent call = %+v, want program opencode-ai in commander repo", rc)
	}
	if len(reg.EnsureCalls) != 1 || reg.EnsureCalls[0] != "/tmp/example/code/commander" {
		t.Errorf("EnsureProject calls = %v, want [commander repo]", reg.EnsureCalls)
	}
	if res.AmAgentResolvedName != "GoldTiger" {
		t.Errorf("AmAgentResolvedName = %q, want GoldTiger", res.AmAgentResolvedName)
	}
	// Both the manager role and the legacy lead alias map to the one assigned name.
	for _, role := range []string{"manager-frontend", "lead-frontend"} {
		if got, _ := fs.GetAmAgent(context.Background(), "/tmp/example/code/commander", role); got != "GoldTiger" {
			t.Errorf("mapping[%s] = %q, want GoldTiger", role, got)
		}
	}

	// .commander/ exists.
	if _, err := os.Stat(filepath.Join(dir, ".commander")); err != nil {
		t.Errorf(".commander dir: %v", err)
	}

	// .git/info/exclude was updated.
	excl, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("read exclude: %v", err)
	}
	for _, want := range []string{".commander/", ".worktrees/"} {
		if !strings.Contains(string(excl), want) {
			t.Errorf("exclude missing %q", want)
		}
	}

	if len(oc.RunCalls) != 0 {
		t.Fatalf("opencode.RunCalls = %d, want 0 (blank import path)", len(oc.RunCalls))
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "opencode", "agents", "manager-frontend.md")); err != nil {
		t.Errorf("manager agent prompt missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".commander", "manager.session-id")); err != nil {
		t.Errorf("manager session file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".commander", "lead.session-id")); err != nil {
		t.Errorf("legacy lead session file missing during migration: %v", err)
	}
}

func TestActivate_Idempotent_ExistingSessionFile(t *testing.T) {
	dir := makeGitDir(t)
	// Pre-populate session-id file.
	if err := os.MkdirAll(filepath.Join(dir, ".commander"), 0o755); err != nil {
		t.Fatalf("mkdir .commander: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".commander", "lead.session-id"), []byte("ses_REUSED__________________ABCD\n"), 0o644); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	fs := newFakeStore()
	oc := &opencode.MockClient{
		// Should never be called; if it is, force a visible failure.
		RunErr: errors.New("opencode.Run should not have been called"),
	}
	reg := &agentmail.MockRegistrar{NextName: "GoldTiger"}
	a := New(fs, oc, reg, "/tmp/example/code/commander")
	origNormalize := normalizeManagerSessionFn
	normalizeManagerSessionFn = func(_ context.Context, _ ActivateRequest, sessionID string) (string, bool, error) {
		return sessionID, false, nil
	}
	t.Cleanup(func() { normalizeManagerSessionFn = origNormalize })

	res, err := a.Activate(context.Background(), ActivateRequest{
		Name: "survey", Type: "knowledge", Path: dir,
	})
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if res.LeadSessionID != "ses_REUSED__________________ABCD" {
		t.Errorf("LeadSessionID = %q, want reused id", res.LeadSessionID)
	}
	if !res.AlreadyActive {
		t.Error("AlreadyActive = false, want true (session file pre-existed)")
	}
	if len(oc.RunCalls) != 0 {
		t.Errorf("opencode.Run was called %d times, want 0 (id reused)", len(oc.RunCalls))
	}
	if len(reg.RegisterCalls) != 1 {
		t.Errorf("RegisterCalls = %d, want 1 (one agent, both roles mapped)", len(reg.RegisterCalls))
	}
	if body, err := os.ReadFile(filepath.Join(dir, ".commander", "manager.session-id")); err != nil {
		t.Errorf("manager session file should be backfilled: %v", err)
	} else if strings.TrimSpace(string(body)) != "ses_REUSED__________________ABCD" {
		t.Errorf("manager session file = %q", strings.TrimSpace(string(body)))
	}
}

func TestActivate_Idempotent_DuplicateProjectName(t *testing.T) {
	dir := makeGitDir(t)
	fs := newFakeStore()
	// Seed: same name already in DB.
	if _, err := fs.InsertProject(context.Background(), store.Project{
		Name: "frontend", Type: "code", Path: dir, ActivatedAt: 1, Status: "active",
	}); err != nil {
		t.Fatalf("seed insert: %v", err)
	}

	oc := &opencode.MockClient{}
	reg := &agentmail.MockRegistrar{NextName: "GoldTiger"}
	a := New(fs, oc, reg, "/tmp/example/code/commander")
	origCapture := captureManagerSessionFn
	origNormalize := normalizeManagerSessionFn
	captureManagerSessionFn = func(context.Context, ActivateRequest, string) (string, error) {
		return "ses_NEWCAPTURE_____________ABCD", nil
	}
	normalizeManagerSessionFn = func(_ context.Context, _ ActivateRequest, sessionID string) (string, bool, error) {
		return sessionID, false, nil
	}
	t.Cleanup(func() {
		captureManagerSessionFn = origCapture
		normalizeManagerSessionFn = origNormalize
	})

	res, err := a.Activate(context.Background(), ActivateRequest{
		Name: "frontend", Type: "code", Path: dir,
	})
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if !res.AlreadyActive {
		t.Error("AlreadyActive = false, want true (DB row already present)")
	}
	if res.ProjectID != 1 {
		t.Errorf("ProjectID = %d, want 1 (existing row)", res.ProjectID)
	}
}

func TestActivate_BootstrapFailure_DoesNotPersistOrRegister(t *testing.T) {
	dir := makeGitDir(t)
	fs := newFakeStore()
	oc := &opencode.MockClient{}
	reg := &agentmail.MockRegistrar{NextName: "GoldTiger"}
	a := New(fs, oc, reg, "/tmp/example/code/commander")
	origCapture := captureManagerSessionFn
	origNormalize := normalizeManagerSessionFn
	captureManagerSessionFn = func(context.Context, ActivateRequest, string) (string, error) {
		return "", errors.New("opencode boom")
	}
	normalizeManagerSessionFn = func(_ context.Context, _ ActivateRequest, sessionID string) (string, bool, error) {
		return sessionID, false, nil
	}
	t.Cleanup(func() {
		captureManagerSessionFn = origCapture
		normalizeManagerSessionFn = origNormalize
	})

	_, err := a.Activate(context.Background(), ActivateRequest{
		Name: "frontend", Type: "code", Path: dir,
	})
	if err == nil {
		t.Fatal("expected error when opencode bootstrap fails")
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".commander", "manager.session-id")); !os.IsNotExist(statErr) {
		t.Errorf("manager session-id file exists after bootstrap failure: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".commander", "lead.session-id")); !os.IsNotExist(statErr) {
		t.Errorf("legacy lead session-id file exists after bootstrap failure: %v", statErr)
	}
	if len(reg.RegisterCalls) != 0 {
		t.Errorf("RegisterAgent called %d times after bootstrap failure, want 0", len(reg.RegisterCalls))
	}
}

func TestActivate_Validation(t *testing.T) {
	goodDir := makeGitDir(t)
	cases := []struct {
		name   string
		req    ActivateRequest
		errSub string
	}{
		{
			name:   "empty name",
			req:    ActivateRequest{Name: "", Type: "code", Path: goodDir},
			errSub: "must be lowercase",
		},
		{
			name:   "uppercase name",
			req:    ActivateRequest{Name: "Frontend", Type: "code", Path: goodDir},
			errSub: "must be lowercase",
		},
		{
			name:   "bad type",
			req:    ActivateRequest{Name: "x", Type: "music", Path: goodDir},
			errSub: "type",
		},
		{
			name:   "relative path",
			req:    ActivateRequest{Name: "x", Type: "code", Path: "rel/path"},
			errSub: "absolute",
		},
		{
			name:   "missing path",
			req:    ActivateRequest{Name: "x", Type: "code", Path: "/nonexistent/path/xyz123"},
			errSub: "no such file",
		},
		{
			name:   "code without git",
			req:    ActivateRequest{Name: "x", Type: "code", Path: t.TempDir()},
			errSub: "not a git repo",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeStore()
			oc := &opencode.MockClient{}
			reg := &agentmail.MockRegistrar{NextName: "GoldTiger"}
			a := New(fs, oc, reg, "/tmp/example/code/commander")
			_, err := a.Activate(context.Background(), tc.req)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.errSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.errSub)
			}
			if len(oc.RunCalls) != 0 {
				t.Errorf("opencode invoked despite validation failure: %d calls", len(oc.RunCalls))
			}
			if len(reg.RegisterCalls) != 0 {
				t.Errorf("am invoked despite validation failure: %d calls", len(reg.RegisterCalls))
			}
		})
	}
}

func TestActivate_KnowledgeTypeWithoutGit(t *testing.T) {
	dir := t.TempDir() // no .git/
	fs := newFakeStore()
	oc := &opencode.MockClient{}
	reg := &agentmail.MockRegistrar{NextName: "GoldTiger"}
	a := New(fs, oc, reg, "/tmp/example/code/commander")
	origCapture := captureManagerSessionFn
	origNormalize := normalizeManagerSessionFn
	captureManagerSessionFn = func(context.Context, ActivateRequest, string) (string, error) {
		return "ses_KNOWLEDGE______________ABCD", nil
	}
	normalizeManagerSessionFn = func(_ context.Context, _ ActivateRequest, sessionID string) (string, bool, error) {
		return sessionID, false, nil
	}
	t.Cleanup(func() {
		captureManagerSessionFn = origCapture
		normalizeManagerSessionFn = origNormalize
	})

	res, err := a.Activate(context.Background(), ActivateRequest{
		Name: "survey", Type: "knowledge", Path: dir,
	})
	if err != nil {
		t.Fatalf("knowledge-type without .git should succeed: %v", err)
	}
	if res.LeadSessionID == "" {
		t.Error("expected non-empty session id")
	}
}

func TestMarshalManagerBlankSession_NoBootstrapMessages(t *testing.T) {
	now := time.UnixMilli(1778448655181)
	raw, err := marshalManagerBlankSession("ses_mgr_test", ActivateRequest{
		Name: "manifest",
		Type: "code",
		Path: "/tmp/example/code/manifest",
	}, now)
	if err != nil {
		t.Fatalf("marshalManagerBlankSession: %v", err)
	}

	var got struct {
		Info struct {
			ID        string `json:"id"`
			Slug      string `json:"slug"`
			ProjectID string `json:"projectID"`
			Directory string `json:"directory"`
			Path      string `json:"path"`
			Title     string `json:"title"`
			Version   string `json:"version"`
		} `json:"info"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if got.Info.ID != "ses_mgr_test" {
		t.Fatalf("id = %q", got.Info.ID)
	}
	if got.Info.Slug != "manager-manifest" {
		t.Fatalf("slug = %q", got.Info.Slug)
	}
	if got.Info.ProjectID != "manifest" {
		t.Fatalf("projectID = %q", got.Info.ProjectID)
	}
	if got.Info.Directory != "/tmp/example/code/manifest" {
		t.Fatalf("directory = %q", got.Info.Directory)
	}
	if got.Info.Path != "tmp/example/code/manifest" {
		t.Fatalf("path = %q", got.Info.Path)
	}
	if got.Info.Title != "Manager · manifest" {
		t.Fatalf("title = %q", got.Info.Title)
	}
	if got.Info.Version != "0" {
		t.Fatalf("version = %q", got.Info.Version)
	}
	if len(got.Messages) != 0 {
		t.Fatalf("messages len = %d, want 0", len(got.Messages))
	}
	if strings.Contains(string(raw), "bootstrap session") {
		t.Fatalf("blank manager session payload should not embed a bootstrap chat: %s", raw)
	}
}

func TestSessionNeedsManagerBootstrapMigration(t *testing.T) {
	raw := []byte(`{
  "messages": [
    {
      "info": {"role":"user"},
      "parts": [{"type":"text","text":"\"You are the Lead for project \\\"commander\\\" (type=code, path=/tmp/example/code/commander). This is your bootstrap session. Reply with a one-line acknowledgement.\""}]
    },
    {
      "info": {"role":"assistant"},
      "parts": [
        {"type":"reasoning","text":"reasoning"},
        {"type":"text","text":"Acknowledged — Lead for project \"commander\" bootstrapped and ready."}
      ]
    }
  ]
}`)
	if !sessionNeedsManagerBootstrapMigration(raw) {
		t.Fatal("expected bootstrap-only manager session to need migration")
	}
}

func TestSessionNeedsManagerBootstrapMigration_FalseForRealConversation(t *testing.T) {
	raw := []byte(`{
  "messages": [
    {
      "info": {"role":"user"},
      "parts": [{"type":"text","text":"show me objectives"}]
    },
    {
      "info": {"role":"assistant"},
      "parts": [{"type":"text","text":"No objectives yet."}]
    },
    {
      "info": {"role":"user"},
      "parts": [{"type":"text","text":"create one"}]
    }
  ]
}`)
	if sessionNeedsManagerBootstrapMigration(raw) {
		t.Fatal("did not expect a real conversation to need migration")
	}
}

func TestActivate_MigratesBootstrapOnlyManagerSession(t *testing.T) {
	dir := makeGitDir(t)
	if err := os.MkdirAll(filepath.Join(dir, ".commander"), 0o755); err != nil {
		t.Fatalf("mkdir .commander: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".commander", "manager.session-id"), []byte("ses_legacy_bootstrap\n"), 0o644); err != nil {
		t.Fatalf("seed manager session: %v", err)
	}

	fs := newFakeStore()
	oc := &opencode.MockClient{}
	reg := &agentmail.MockRegistrar{NextName: "GoldTiger"}
	a := New(fs, oc, reg, "/tmp/example/code/commander")
	origNormalize := normalizeManagerSessionFn
	normalizeManagerSessionFn = func(_ context.Context, _ ActivateRequest, sessionID string) (string, bool, error) {
		if sessionID != "ses_legacy_bootstrap" {
			t.Fatalf("normalize got session %q", sessionID)
		}
		return "ses_blank_replacement", true, nil
	}
	t.Cleanup(func() { normalizeManagerSessionFn = origNormalize })

	res, err := a.Activate(context.Background(), ActivateRequest{
		Name: "manifest", Type: "code", Path: dir,
	})
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if res.LeadSessionID != "ses_blank_replacement" {
		t.Fatalf("LeadSessionID = %q", res.LeadSessionID)
	}
	for _, name := range []string{"manager.session-id", "lead.session-id"} {
		body, err := os.ReadFile(filepath.Join(dir, ".commander", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.TrimSpace(string(body)) != "ses_blank_replacement" {
			t.Fatalf("%s = %q", name, strings.TrimSpace(string(body)))
		}
	}
}

func TestAppendGitExcludes_Idempotent(t *testing.T) {
	dir := makeGitDir(t)
	if err := appendGitExcludes(dir); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := appendGitExcludes(dir); err != nil {
		t.Fatalf("second append: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(body)
	if c := strings.Count(got, ".commander/"); c != 1 {
		t.Errorf(".commander/ appeared %d times, want 1", c)
	}
	if c := strings.Count(got, ".worktrees/"); c != 1 {
		t.Errorf(".worktrees/ appeared %d times, want 1", c)
	}
}

func TestFlatName(t *testing.T) {
	cases := map[string]string{
		"frontend":   "frontend",
		"My Project": "my-project",
		"foo_bar":    "foo-bar",
		"a/b/c":      "a-b-c",
	}
	for in, want := range cases {
		if got := flatName(in); got != want {
			t.Errorf("flatName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestActivate_ClaudeRuntimeMintsManagerSession(t *testing.T) {
	t.Setenv(agentrt.EnvVar, string(agentrt.Claude))
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := makeGitDir(t)
	useMockBeads(t)
	origCapture := captureManagerSessionFn
	captureManagerSessionFn = func(context.Context, ActivateRequest, string) (string, error) {
		return "", errors.New("opencode capture must not run under the claude runtime")
	}
	t.Cleanup(func() { captureManagerSessionFn = origCapture })

	a := New(newFakeStore(), &opencode.MockClient{}, &agentmail.MockRegistrar{NextName: "GoldTiger"}, "/tmp/example/code/commander")
	req := ActivateRequest{Name: "frontend", Type: "code", Path: dir}
	res, err := a.Activate(context.Background(), req)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if want := filepath.Join(dir, ".commander", "manager.claude.session-id"); res.LeadSessionIDPath != want {
		t.Errorf("LeadSessionIDPath = %q, want %q", res.LeadSessionIDPath, want)
	}
	if len(res.LeadSessionID) != 36 {
		t.Errorf("LeadSessionID = %q, want a UUID", res.LeadSessionID)
	}
	if _, err := os.Stat(filepath.Join(dir, ".commander", "manager.session-id")); !os.IsNotExist(err) {
		t.Errorf("opencode manager session file should not be written")
	}
	if _, err := os.Stat(filepath.Join(home, "claude-agents", "manager-frontend.md")); err != nil {
		t.Errorf("claude manager prompt missing: %v", err)
	}

	again, err := a.Activate(context.Background(), req)
	if err != nil {
		t.Fatalf("re-Activate: %v", err)
	}
	if again.LeadSessionID != res.LeadSessionID || !again.AlreadyActive {
		t.Errorf("re-activation should reuse the session: got %q (already=%v), want %q", again.LeadSessionID, again.AlreadyActive, res.LeadSessionID)
	}
}
