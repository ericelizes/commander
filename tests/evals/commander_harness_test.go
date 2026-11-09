package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/layout"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/testguard"
	"github.com/example-user/commander/internal/tmuxctl"
)

// startStubAmServer stands in for `am serve-http` so eval runs never touch a
// real Agent Mail server. It answers MCP tools/call for the tools activation
// uses, returning canned successful payloads in the JSON-RPC envelope the real
// server uses (result.content[0].text). Without this, a real cmdr's activation
// would register agents against the developer's live am server.
func startStubAmServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     string `json:"id"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		text := "{}"
		switch req.Params.Name {
		case "ensure_project":
			text = `{"slug":"eval-stub","human_key":"eval"}`
		case "register_agent":
			text = `{"id":1,"name":"EvalStubAgent","program":"opencode-ai","model":"sonnet"}`
		case "list_agents":
			text = `[]`
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": text}},
				"isError": false,
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

type scenario struct {
	Name   string         `json:"name"`
	Setup  scenarioSetup  `json:"setup"`
	Turns  []scenarioTurn `json:"turns"`
	Expect scenarioExpect `json:"expect"`
}

type scenarioSetup struct {
	ActivateProject   bool                       `json:"activate_project"`
	AmbiguousProject  bool                       `json:"ambiguous_project"`
	WorkspaceProjects []scenarioWorkspaceProject `json:"workspace_projects"`
}

type scenarioWorkspaceProject struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type scenarioTurn struct {
	Input         string   `json:"input"`
	ReplyContains []string `json:"reply_contains"`
	ReplyExcludes []string `json:"reply_excludes"`
}

type scenarioExpect struct {
	ActionContains  []string `json:"action_contains"`
	ActionExcludes  []string `json:"action_excludes"`
	SearchCount     *int     `json:"search_count"`
	CockpitContains []string `json:"cockpit_contains"`
	SelectedWindow  string   `json:"selected_window"`
	Projects        []string `json:"projects"`
	LeadSession     bool     `json:"lead_session"`
}

type harness struct {
	t             *testing.T
	ctx           context.Context
	cancel        context.CancelFunc
	tmp           string
	binDir        string
	realCmdrBin   string
	cmdrBin       string
	opencodeBin   string
	homeDir       string
	configDir     string
	workspaceRoot string
	projectPath   string
	projectPaths  map[string]string
	tmuxTmpDir    string
	transcriptLog string
	actionLog     string
	searchLog     string
	runLog        string
	chatTarget    string
	scriptedMode  bool
	baseEnv       []string
}

func TestCommanderEvalScenarios(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	files, err := filepath.Glob(filepath.Join(scenariosDir(), "*.json"))
	if err != nil {
		t.Fatalf("glob scenarios: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no scenario files found")
	}

	for _, file := range files {
		file := file
		t.Run(strings.TrimSuffix(filepath.Base(file), ".json"), func(t *testing.T) {
			sc := loadScenario(t, file)
			h := newHarness(t, false, sc.Setup)
			defer h.close()
			h.runScenario(sc)
		})
	}
}

func TestCommanderLiveSmoke(t *testing.T) {
	if os.Getenv("COMMANDER_LIVE_SMOKE") != "1" {
		t.Skip("set COMMANDER_LIVE_SMOKE=1 to run against real opencode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	sc := loadScenario(t, filepath.Join(scenariosDir(), "who-are-you.json"))
	h := newHarness(t, true, sc.Setup)
	defer h.close()
	h.runScenario(sc)
}

func loadScenario(t *testing.T, path string) scenario {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read scenario %s: %v", path, err)
	}
	var sc scenario
	if err := json.Unmarshal(body, &sc); err != nil {
		t.Fatalf("parse scenario %s: %v", path, err)
	}
	return sc
}

func newHarness(t *testing.T, live bool, setup scenarioSetup) *harness {
	t.Helper()
	h := &harness{
		t:            t,
		tmp:          t.TempDir(),
		scriptedMode: !live,
	}
	testguard.NoLeakedProcesses(t, h.tmp)
	h.binDir = filepath.Join(h.tmp, "bin")
	h.homeDir = filepath.Join(h.tmp, "home")
	h.configDir = filepath.Join(h.tmp, "xdg")
	h.workspaceRoot = filepath.Join(h.tmp, "workspace")
	h.projectPath = filepath.Join(h.workspaceRoot, "commander")
	h.projectPaths = map[string]string{
		"commander": h.projectPath,
	}
	h.transcriptLog = filepath.Join(h.tmp, "transcript.log")
	h.actionLog = filepath.Join(h.tmp, "actions.log")
	h.searchLog = filepath.Join(h.tmp, "search.log")
	h.runLog = filepath.Join(h.tmp, "runs.log")

	mustMkdirAll(t, h.binDir)
	mustMkdirAll(t, h.homeDir)
	mustMkdirAll(t, h.configDir)
	var err error
	// tmux socket paths have a ~108-char limit, so use a short base dir.
	// macOS Go temp dirs (/var/folders/...) are too long; /private/tmp is the
	// short macOS equivalent of Linux's /tmp.
	tmuxTmpBase := "/tmp"
	if runtime.GOOS == "darwin" {
		tmuxTmpBase = "/private/tmp"
	}
	h.tmuxTmpDir, err = os.MkdirTemp(tmuxTmpBase, "cmdr-tmux-")
	if err != nil {
		t.Fatalf("create tmux temp dir: %v", err)
	}
	// Registered before anything can fail: killing only the commander session
	// (h.close) left a server behind whenever setup failed, and never ended the
	// Operators' server. Both run every pane they hold until killed.
	t.Cleanup(func() {
		// By absolute socket: -S has no fallback to the real servers in /tmp.
		for _, name := range []string{"default", tmuxctl.OpsSocket} {
			ctx, cancel := context.WithTimeout(context.Background(), evalTimeout(t, 5*time.Second))
			cmd := exec.CommandContext(ctx, "tmux", "-S", testguard.TmuxSocket(h.tmuxTmpDir, name), "kill-server")
			cmd.Env = append(os.Environ(), "TMUX=", "TMUX_TMPDIR="+h.tmuxTmpDir)
			_ = cmd.Run()
			cancel()
		}
		_ = os.RemoveAll(h.tmuxTmpDir)
	})
	mustMkdirAll(t, h.projectPath)
	mustMkdirAll(t, filepath.Join(h.projectPath, ".git"))
	for _, project := range setup.WorkspaceProjects {
		name := strings.TrimSpace(project.Name)
		if name == "" {
			continue
		}
		path := filepath.Join(h.workspaceRoot, name)
		h.projectPaths[name] = path
		mustMkdirAll(t, path)
		if strings.EqualFold(project.Type, "code") {
			mustMkdirAll(t, filepath.Join(path, ".git"))
		}
	}
	if setup.AmbiguousProject {
		mustMkdirAll(t, filepath.Join(h.workspaceRoot, "nested", "commander", ".git"))
	}

	h.baseEnv = append(os.Environ(),
		"PATH="+h.binDir+":"+os.Getenv("PATH"),
		"COMMANDER_HOME="+h.homeDir,
		"COMMANDER_REPO="+repoRoot(),
		"XDG_CONFIG_HOME="+h.configDir,
		"COMMANDER_WORKSPACE_ROOT="+h.workspaceRoot,
		"TMUX_TMPDIR="+h.tmuxTmpDir,
		"FAKE_OPENCODE_TRANSCRIPT_LOG="+h.transcriptLog,
		"FAKE_OPENCODE_ACTION_LOG="+h.actionLog,
		"FAKE_OPENCODE_SEARCH_LOG="+h.searchLog,
		"FAKE_OPENCODE_RUN_LOG="+h.runLog,
	)
	t.Setenv("PATH", h.binDir+":"+os.Getenv("PATH"))
	t.Setenv("COMMANDER_HOME", h.homeDir)
	t.Setenv("COMMANDER_REPO", repoRoot())
	t.Setenv("XDG_CONFIG_HOME", h.configDir)
	t.Setenv("COMMANDER_WORKSPACE_ROOT", h.workspaceRoot)
	t.Setenv("TMUX_TMPDIR", h.tmuxTmpDir)
	// An inherited $TMUX makes tmux ignore TMUX_TMPDIR and talk to the caller's
	// server, so killSession would kill the user's real commander session.
	t.Setenv("TMUX", "")
	t.Setenv("FAKE_OPENCODE_TRANSCRIPT_LOG", h.transcriptLog)
	t.Setenv("FAKE_OPENCODE_ACTION_LOG", h.actionLog)
	t.Setenv("FAKE_OPENCODE_SEARCH_LOG", h.searchLog)
	t.Setenv("FAKE_OPENCODE_RUN_LOG", h.runLog)
	// Isolate agent-mail: point the cmdr subprocess at a stub am server so eval
	// runs never register agents against a real Agent Mail server.
	t.Setenv("AM_HTTP_URL", startStubAmServer(t))
	t.Setenv("AM_HTTP_BEARER_TOKEN", "eval-stub-token")
	h.baseEnv = os.Environ()

	h.buildBinaries(live)
	// Compiling has its own budget; do not spend the scenario's time on it.
	h.ctx, h.cancel = context.WithTimeout(context.Background(), evalTimeout(t, 20*time.Second))
	t.Cleanup(h.cancel)
	h.killSession()
	if err := os.WriteFile(filepath.Join(h.homeDir, layout.CommanderSessionFileName), []byte("ses_commander\n"), 0o644); err != nil {
		t.Fatalf("seed commander session id: %v", err)
	}
	d := layout.NewDirector()
	d.SetBinaries(h.cmdrBin, h.opencodeBin)
	if err := d.LaunchCommander(h.ctx, h.homeDir, false); err != nil {
		t.Fatalf("launch commander: %v", err)
	}
	h.chatTarget = h.resolveChatTarget()
	if setup.ActivateProject {
		h.runCmdr("internal-add-project", "--name", "commander", "--type", "code", "--path", h.projectPath)
		h.resetLogs()
	}
	return h
}

func (h *harness) buildBinaries(live bool) {
	h.realCmdrBin = filepath.Join(h.binDir, "cmdr-real")
	mustBuild(h.t, h.baseEnv, h.realCmdrBin, "./cmd/cmdr")
	h.cmdrBin = filepath.Join(h.binDir, "cmdr")
	writeExecutable(h.t, h.cmdrBin, fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"__column\" ]; then\n  printf 'harness column\\n'\n  exec sleep 3600\nfi\nexec %s \"$@\"\n", h.realCmdrBin))
	writeExecutable(h.t, filepath.Join(h.binDir, "am"), "#!/bin/sh\nprintf '{\"ok\":true}\\n'\n")
	if live {
		h.opencodeBin = "opencode"
		return
	}
	h.opencodeBin = filepath.Join(h.binDir, "opencode")
	mustBuild(h.t, h.baseEnv, h.opencodeBin, "./tests/evals/fakeopencode")
}

func mustBuild(t *testing.T, env []string, out, pkg string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), evalTimeout(t, 30*time.Second))
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", out, pkg)
	cmd.Env = env
	cmd.Dir = repoRoot()
	if outBytes, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s failed: %v\n%s", pkg, err, string(outBytes))
	}
}

func repoRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func scenariosDir() string {
	return filepath.Join(repoRoot(), "tests", "evals", "scenarios")
}

func (h *harness) runScenario(sc scenario) {
	if len(sc.Expect.CockpitContains) > 0 {
		h.tmux("resize-window", "-t", "commander", "-x", "240", "-y", "120")
		h.tmux("resize-pane", "-t", h.chatTarget, "-x", "160", "-y", "100")
	}
	for _, turn := range sc.Turns {
		reply := h.send(turn.Input)
		for _, needle := range turn.ReplyContains {
			if !strings.Contains(reply, h.expand(needle)) {
				h.t.Fatalf("reply %q missing %q", reply, h.expand(needle))
			}
		}
		for _, needle := range turn.ReplyExcludes {
			if strings.Contains(reply, h.expand(needle)) {
				h.t.Fatalf("reply %q unexpectedly contains %q", reply, h.expand(needle))
			}
		}
	}

	if len(sc.Expect.CockpitContains) > 0 {
		// This harness owns its server; the shell driver refuses to adopt it.
		var raw strings.Builder
		for _, pane := range strings.Fields(h.tmux("list-panes", "-t", "commander", "-F", "#{pane_id}")) {
			raw.WriteString(h.tmux("capture-pane", "-t", pane, "-p"))
		}
		for _, want := range sc.Expect.CockpitContains {
			if !strings.Contains(raw.String(), want) {
				h.t.Fatalf("cockpit missing %q: %s", want, raw.String())
			}
		}
		h.t.Logf("live cockpit text:\n%s", raw.String())
	}
	actions := h.readLines(h.actionLog)
	for _, needle := range sc.Expect.ActionContains {
		if !containsLine(actions, h.expand(needle)) {
			h.t.Fatalf("actions %v missing %q", actions, h.expand(needle))
		}
	}
	for _, needle := range sc.Expect.ActionExcludes {
		if containsLine(actions, h.expand(needle)) {
			h.t.Fatalf("actions %v unexpectedly contain %q", actions, h.expand(needle))
		}
	}
	if sc.Expect.SearchCount != nil {
		if got := len(h.readLines(h.searchLog)); got != *sc.Expect.SearchCount {
			h.t.Fatalf("search count=%d want %d", got, *sc.Expect.SearchCount)
		}
	}
	if sc.Expect.SelectedWindow != "" {
		if got := h.selectedWindow(); got != sc.Expect.SelectedWindow {
			h.t.Fatalf("selected window=%q want %q", got, sc.Expect.SelectedWindow)
		}
	}
	if len(sc.Expect.Projects) > 0 {
		if got := h.projectNames(); strings.Join(got, ",") != strings.Join(sc.Expect.Projects, ",") {
			h.t.Fatalf("projects=%v want %v", got, sc.Expect.Projects)
		}
	}
	if sc.Expect.LeadSession {
		if _, err := os.Stat(filepath.Join(h.projectPath, ".commander", "manager.session-id")); err != nil {
			h.t.Fatalf("manager session file missing: %v", err)
		}
	}
}

func (h *harness) send(msg string) string {
	before := len(h.assistantReplies())
	h.tmux("send-keys", "-t", h.chatTarget, "-l", h.expand(msg))
	h.tmux("send-keys", "-t", h.chatTarget, "Enter")
	ctx, cancel := context.WithTimeout(h.ctx, evalTimeout(h.t, 5*time.Second))
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		replies := h.assistantReplies()
		if len(replies) > before {
			return replies[len(replies)-1]
		}
		select {
		case <-ctx.Done():
			if h.scriptedMode {
				h.t.Fatalf("timed out waiting for scripted reply to %q: %v", msg, ctx.Err())
			}
			return h.capturePane()
		case <-ticker.C:
		}
	}
}

func (h *harness) assistantReplies() []string {
	lines := h.readLines(h.transcriptLog)
	var replies []string
	for _, line := range lines {
		if strings.HasPrefix(line, "assistant: ") {
			replies = append(replies, strings.TrimPrefix(line, "assistant: "))
		}
	}
	return replies
}

func (h *harness) readLines(path string) []string {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		h.t.Fatalf("read %s: %v", path, err)
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func (h *harness) selectedWindow() string {
	out := h.tmux("display-message", "-p", "-t", layout.CommanderSessionName, "#W")
	return strings.TrimSpace(out)
}

func (h *harness) projectNames() []string {
	st, err := store.Open(filepath.Join(h.homeDir, "state.db"))
	if err != nil {
		h.t.Fatalf("open state.db: %v", err)
	}
	defer st.Close()
	projects, err := st.ListProjects(context.Background())
	if err != nil {
		h.t.Fatalf("list projects: %v", err)
	}
	names := make([]string, 0, len(projects))
	for _, project := range projects {
		names = append(names, project.Name)
	}
	return names
}

func (h *harness) runCmdr(args ...string) string {
	cmd := exec.CommandContext(h.ctx, h.realCmdrBin, args...)
	cmd.Env = h.baseEnv
	cmd.Dir = repoRoot()
	out, err := cmd.CombinedOutput()
	if err != nil {
		h.t.Fatalf("cmdr %v failed: %v\n%s", args, err, string(out))
	}
	return string(out)
}

func (h *harness) capturePane() string {
	return strings.TrimSpace(h.tmux("capture-pane", "-p", "-t", h.chatTarget))
}

func (h *harness) tmux(args ...string) string {
	cmd := exec.CommandContext(h.ctx, "tmux", args...)
	cmd.Env = h.baseEnv
	out, err := cmd.CombinedOutput()
	if err != nil {
		h.t.Fatalf("tmux %v failed: %v\n%s", args, err, string(out))
	}
	return string(out)
}

func (h *harness) resetLogs() {
	for _, path := range []string{h.transcriptLog, h.actionLog, h.searchLog} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			h.t.Fatalf("remove %s: %v", path, err)
		}
	}
}

func (h *harness) killSession() {
	ctx, cancel := context.WithTimeout(context.Background(), evalTimeout(h.t, 5*time.Second))
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", "-S", testguard.TmuxSocket(h.tmuxTmpDir, "default"), "kill-session", "-t", layout.CommanderSessionName)
	cmd.Env = h.baseEnv
	_, _ = cmd.CombinedOutput()
}

// close ends the commander session; the servers and their socket dir go in
// newHarness's cleanup (removing the dir first would orphan a live server).
func (h *harness) close() {
	h.killSession()
	h.cancel()
}

func (h *harness) resolveChatTarget() string {
	out := h.tmux("list-panes", "-t", layout.CommanderSessionName+":"+layout.FleetWindowName, "-F", "#{pane_id} #{pane_current_command}")
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.Contains(fields[1], "opencode") {
			return fields[0]
		}
	}
	h.t.Fatalf("could not resolve chat pane from output: %s", out)
	return ""
}

func (h *harness) expand(s string) string {
	s = strings.ReplaceAll(s, "{{project_path}}", h.projectPath)
	s = strings.ReplaceAll(s, "{{workspace_root}}", h.workspaceRoot)
	for name, path := range h.projectPaths {
		s = strings.ReplaceAll(s, "{{"+name+"_path}}", path)
	}
	return s
}

func containsLine(lines []string, want string) bool {
	for _, line := range lines {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
