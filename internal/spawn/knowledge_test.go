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

// newKnowledgeSpawner builds a Spawner wired with mock tmux + a deterministic
// fifo capture stub. Callers tweak fields after construction.
func newKnowledgeSpawner(
	t *testing.T,
	capturedID string,
) (*Spawner, *fakeStore, *beads.MockClient, *agentmail.MockClient, *tmuxctl.MockClient) {
	t.Helper()
	fs := newFakeStore()
	oc := &opencode.MockClient{}
	bd := &beads.MockClient{}
	am := &agentmail.MockClient{}
	dk := &docker.MockClient{}
	tx := &tmuxctl.MockClient{}

	commanderRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(commanderRepo, ".opencode", "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	sp := New(fs, oc, bd, am, dk, tx, commanderRepo)
	sp.useHost = func() bool { return false } // the legacy opencode flow; host: repo_path_test.go
	sp.now = func() time.Time { return time.Unix(1700000000, 0) }
	// Knowledge tests inject a stub for fifo-based capture so we never block
	// on a real reader/writer pairing.
	sp.captureKnowledgeSession = func(_ context.Context, _ string, _ time.Duration) (string, error) {
		return capturedID, nil
	}
	return sp, fs, bd, am, tx
}

func goodKnowledgeRequest(t *testing.T, projectPath string) SpawnRequest {
	t.Helper()
	return SpawnRequest{
		ProjectID:   77,
		ProjectName: "survey",
		ProjectPath: projectPath,
		ProjectType: "knowledge",
		TaskBeadID:  "cmdr-4fc",
		Model:       "anthropic/claude-sonnet-4-5",
		// Branch intentionally unset — knowledge spawn ignores it.
	}
}

func TestSpawn_DispatchesByProjectType(t *testing.T) {
	withTempHome(t)
	sp, _, _, _, _ := newKnowledgeSpawner(t, "ses_x")

	req := SpawnRequest{
		ProjectID:   1,
		ProjectName: "p",
		ProjectPath: "/tmp",
		ProjectType: "weather",
		TaskBeadID:  "bd-1",
		Model:       "m",
	}
	if _, err := sp.Spawn(context.Background(), req); err == nil {
		t.Fatal("expected dispatch error on unknown ProjectType")
	} else if !strings.Contains(err.Error(), "ProjectType") {
		t.Errorf("error wording: %v", err)
	}
}

func TestSpawnKnowledge_HappyPath(t *testing.T) {
	withTempHome(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := makeGitRepo(t)

	sp, fs, bd, am, tx := newKnowledgeSpawner(t, "ses_kn0wledge")

	res, err := sp.Spawn(context.Background(), goodKnowledgeRequest(t, dir))
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// SpawnResult shape — knowledge has no ContainerID/WorktreePath.
	if res.SessionID != "ses_kn0wledge" {
		t.Errorf("SessionID: got %q", res.SessionID)
	}
	if res.ContainerID != "" {
		t.Errorf("ContainerID should be empty for knowledge: %q", res.ContainerID)
	}
	if res.WorktreePath != "" {
		t.Errorf("WorktreePath should be empty for knowledge: %q", res.WorktreePath)
	}
	if res.AgentName != "operator-4fc" {
		t.Errorf("AgentName: got %q want operator-4fc", res.AgentName)
	}
	if res.RunID == 0 {
		t.Error("RunID should be non-zero")
	}

	// Fifo present on disk (we removed the stub-cleanup path; fifo persists
	// post-spawn for U10 watchdog consumption).
	fifo := filepath.Join(dir, ".commander", "operators", "4fc.fifo")
	info, err := os.Stat(fifo)
	if err != nil {
		t.Errorf("fifo missing: %v", err)
	} else if info.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("expected named pipe, got mode %v", info.Mode())
	}

	// Session-id file persisted.
	sessionFile := filepath.Join(dir, ".commander", "operators", "4fc.session-id")
	body, err := os.ReadFile(sessionFile)
	if err != nil {
		t.Errorf("session file: %v", err)
	} else if strings.TrimSpace(string(body)) != "ses_kn0wledge" {
		t.Errorf("session file body: %q", string(body))
	}

	// tmux calls in expected order: NewSession → PipePane → SendKeys (no
	// KillPane on success).
	wantPrefixes := []string{"NewSession(", "PipePane(", "SendKeys("}
	if len(tx.Calls) < len(wantPrefixes) {
		t.Fatalf("tmux Calls: got %v", tx.Calls)
	}
	for i, p := range wantPrefixes {
		if !strings.HasPrefix(tx.Calls[i], p) {
			t.Errorf("tmux Calls[%d]: got %q want prefix %q", i, tx.Calls[i], p)
		}
	}
	for _, c := range tx.Calls {
		if strings.HasPrefix(c, "KillPane(") {
			t.Errorf("KillPane should not run on happy path: %v", tx.Calls)
		}
	}

	// tmux session name = cmdr-<project>-<flat-task>.
	wantSession := "cmdr-survey-4fc"
	if !strings.Contains(tx.Calls[0], "name="+wantSession) {
		t.Errorf("session name: got %q want contains %q", tx.Calls[0], wantSession)
	}
	// Pipe-pane wired the fifo we created.
	if !strings.Contains(tx.Calls[1], "fifo="+fifo) {
		t.Errorf("pipe-pane fifo: got %q want fifo=%s", tx.Calls[1], fifo)
	}

	// am.Register called with the right args.
	wantRegister := "Register:survey/opencode-ai/sonnet/operator-4fc"
	found := false
	for _, c := range am.Calls {
		if c == wantRegister {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("am.Register: want %q in %v", wantRegister, am.Calls)
	}

	// bd.SetState called.
	if writes := beadMutations(bd.Calls); len(writes) != 1 || !strings.HasPrefix(writes[0], "SetState:cmdr-4fc:cmdr=brainstorming") {
		t.Errorf("beads.SetState: %v", bd.Calls)
	}

	// agent_runs row: TmuxPaneID populated, ContainerID empty.
	if len(fs.runs) != 1 {
		t.Fatalf("agent_runs: got %d rows want 1", len(fs.runs))
	}
	r := fs.runs[0]
	if r.OpencodeSessionID != "ses_kn0wledge" {
		t.Errorf("OpencodeSessionID: %q", r.OpencodeSessionID)
	}
	if r.ContainerID.Valid {
		t.Errorf("ContainerID should be NULL for knowledge: %+v", r.ContainerID)
	}
	if !r.TmuxPaneID.Valid || r.TmuxPaneID.String != wantSession {
		t.Errorf("TmuxPaneID: got %+v want %q", r.TmuxPaneID, wantSession)
	}
	if r.Status != "running" {
		t.Errorf("status: got %q", r.Status)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "opencode", "agents", "operator-4fc.md")); err != nil {
		t.Errorf("operator knowledge prompt missing: %v", err)
	}
}

func TestSpawnKnowledge_StartsTmuxSessionAtProjectRoot(t *testing.T) {
	withTempHome(t)
	dir := makeGitRepo(t)

	sp, _, _, _, tx := newKnowledgeSpawner(t, "ses_kn0wledge")

	if _, err := sp.Spawn(context.Background(), goodKnowledgeRequest(t, dir)); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if len(tx.Calls) == 0 || !strings.Contains(tx.Calls[0], "cwd="+dir) {
		t.Fatalf("expected first tmux call to start in project root %q; calls=%v", dir, tx.Calls)
	}
}

func TestSpawnKnowledge_ValidationErrors(t *testing.T) {
	withTempHome(t)
	dir := makeGitRepo(t)
	sp, _, _, _, _ := newKnowledgeSpawner(t, "ses_x")

	good := goodKnowledgeRequest(t, dir)

	cases := []struct {
		name string
		mut  func(*SpawnRequest)
	}{
		{"empty model", func(r *SpawnRequest) { r.Model = "" }},
		{"empty task", func(r *SpawnRequest) { r.TaskBeadID = "" }},
		{"empty name", func(r *SpawnRequest) { r.ProjectName = "" }},
		{"relative path", func(r *SpawnRequest) { r.ProjectPath = "relative/path" }},
		{"bad type", func(r *SpawnRequest) { r.ProjectType = "code" }},
	}
	for _, c := range cases {
		req := good
		c.mut(&req)
		// Call spawnKnowledge directly so the dispatch in Spawn doesn't route
		// "code"-typed bad cases to spawnCode.
		if _, err := sp.spawnKnowledge(context.Background(), req); err == nil {
			t.Errorf("%s: expected error", c.name)
		}
	}
}

func TestSpawnKnowledge_Rollback_TmuxNewSessionFails(t *testing.T) {
	withTempHome(t)
	dir := makeGitRepo(t)

	sp, fs, _, am, tx := newKnowledgeSpawner(t, "ses_x")
	tx.NewSessionErr = errors.New("tmux unavailable")

	if _, err := sp.Spawn(context.Background(), goodKnowledgeRequest(t, dir)); err == nil {
		t.Fatal("expected error from tmux NewSession failure")
	}

	// Fifo MUST NOT remain after rollback.
	fifo := filepath.Join(dir, ".commander", "operators", "4fc.fifo")
	if _, err := os.Stat(fifo); err == nil {
		t.Errorf("fifo should be removed after rollback")
	}

	// No agent_runs row.
	if len(fs.runs) != 0 {
		t.Errorf("agent_runs should be empty, got %v", fs.runs)
	}

	// am.Register should not have run.
	for _, c := range am.Calls {
		if strings.HasPrefix(c, "Register:") {
			t.Errorf("am.Register should not run on rollback: %v", am.Calls)
		}
	}
}

func TestSpawnKnowledge_Rollback_CaptureTimeout(t *testing.T) {
	withTempHome(t)
	dir := makeGitRepo(t)

	sp, fs, _, _, tx := newKnowledgeSpawner(t, "")
	sp.captureKnowledgeSession = func(_ context.Context, _ string, _ time.Duration) (string, error) {
		return "", errors.New("session-capture timeout after 30s")
	}

	if _, err := sp.Spawn(context.Background(), goodKnowledgeRequest(t, dir)); err == nil {
		t.Fatal("expected timeout error")
	}

	// Pane killed during rollback.
	killed := false
	for _, c := range tx.Calls {
		if strings.HasPrefix(c, "KillPane(") {
			killed = true
		}
	}
	if !killed {
		t.Errorf("KillPane should have run during rollback: %v", tx.Calls)
	}

	// Fifo removed.
	fifo := filepath.Join(dir, ".commander", "operators", "4fc.fifo")
	if _, err := os.Stat(fifo); err == nil {
		t.Errorf("fifo should be removed after rollback")
	}

	// Session-id file MUST NOT exist (capture failed before write).
	sessionFile := filepath.Join(dir, ".commander", "operators", "4fc.session-id")
	if _, err := os.Stat(sessionFile); err == nil {
		t.Errorf("session-id file should not exist after rollback")
	}

	// No agent_runs row.
	if len(fs.runs) != 0 {
		t.Errorf("agent_runs should be empty, got %v", fs.runs)
	}
}

func TestFlatTask(t *testing.T) {
	cases := map[string]string{
		"cmdr-4fc":         "4fc",
		"cmdr-deadbeef":    "deadbeef",
		"cmdr-1a2b3c4d5e":  "1a2b3c4d", // clipped to 8
		"deadbeef":         "deadbeef",
		"deadbeef00112233": "deadbeef",
		"weird id!":        "weird-id",
		"":                 "task",
		"cmdr-":            "cmdr",
		// Not hex-tailed: kept whole, so beads of one project never collide.
		"example-automation-t09.4.2": "example-automation-t09-4-2",
		"example-automation-t09.4.3": "example-automation-t09-4-3",
	}
	for in, want := range cases {
		if got := flatTask(in); got != want {
			t.Errorf("flatTask(%q): got %q want %q", in, got, want)
		}
	}
}

func TestKnowledgeSessionName(t *testing.T) {
	if got := knowledgeSessionName("survey", "4fc"); got != "cmdr-survey-4fc" {
		t.Errorf("got %q", got)
	}
}

// TestSpawn_KnowledgeMountsReadsExportsEnv asserts that req.MountReads paths
// are exported as COMMANDER_READS=<colon-joined> on the opencode invocation
// the spawner sends into the tmux pane. U19.
func TestSpawn_KnowledgeMountsReadsExportsEnv(t *testing.T) {
	withTempHome(t)
	dir := makeGitRepo(t)

	mountA := filepath.Join(t.TempDir(), "frontend")
	mountB := filepath.Join(t.TempDir(), "sandbox")
	if err := os.MkdirAll(mountA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(mountB, 0o755); err != nil {
		t.Fatal(err)
	}

	sp, _, _, _, tx := newKnowledgeSpawner(t, "ses_x")

	req := goodKnowledgeRequest(t, dir)
	req.MountReads = []string{mountA, mountB}

	if _, err := sp.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Find the SendKeys call. It should prefix the opencode invocation with
	// COMMANDER_READS=<single-quoted colon-joined paths>.
	var sendKeys string
	for _, c := range tx.Calls {
		if strings.HasPrefix(c, "SendKeys(") {
			sendKeys = c
			break
		}
	}
	if sendKeys == "" {
		t.Fatalf("SendKeys not invoked; calls=%v", tx.Calls)
	}
	wantPrefix := "COMMANDER_READS='" + mountA + ":" + mountB + "' opencode run"
	if !strings.Contains(sendKeys, wantPrefix) {
		t.Errorf("SendKeys missing %q; got %q", wantPrefix, sendKeys)
	}
}

// TestSpawn_KnowledgeMountsReadsValidationFailsForNonDir asserts that a
// missing or non-directory MountReads entry fails validation BEFORE any
// tmux side effects. U19.
func TestSpawn_KnowledgeMountsReadsValidationFailsForNonDir(t *testing.T) {
	withTempHome(t)
	dir := makeGitRepo(t)

	sp, fs, _, _, tx := newKnowledgeSpawner(t, "ses_x")

	req := goodKnowledgeRequest(t, dir)
	req.MountReads = []string{filepath.Join(t.TempDir(), "missing")}

	if _, err := sp.Spawn(context.Background(), req); err == nil {
		t.Fatal("expected validation error for missing MountReads path")
	}

	// No tmux calls should have run — validation gates everything.
	if len(tx.Calls) != 0 {
		t.Errorf("tmux must not be called on validation failure; got %v", tx.Calls)
	}
	if len(fs.runs) != 0 {
		t.Errorf("agent_runs should be empty, got %v", fs.runs)
	}

	// Non-directory path also rejected.
	regular := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(regular, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	req.MountReads = []string{regular}
	if _, err := sp.Spawn(context.Background(), req); err == nil {
		t.Error("expected validation error for non-directory MountReads path")
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"hi":           "'hi'",
		"with space":   "'with space'",
		"it's mine":    `'it'\''s mine'`,
		"multi'quote'": `'multi'\''quote'\'''`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q): got %q want %q", in, got, want)
		}
	}
}

// Every bead of a knowledge project gets its own Operator name, session file,
// output file and tmux session: clipping to 8 once gave them all "example-".
func TestFlatTaskNeverCollidesWithinAProject(t *testing.T) {
	seen := map[string]string{}
	for _, id := range []string{"example-automation-t09.4.2", "example-automation-t09.4.3", "example-automation-t08.2.4",
		"example-automation-t08.2.4.1.1.1.1.1.1.1.1.1.1.1", "example-automation-t08.2.4.1.1.1.1.1.1.1.1.1.1.2"} {
		f := flatTask(id)
		if len(f) > maxFlat {
			t.Errorf("flatTask(%q) = %q, longer than %d", id, f, maxFlat)
		}
		if prev, ok := seen[f]; ok {
			t.Errorf("%q and %q both flatten to %q", prev, id, f)
		}
		seen[f] = id
	}
}
