package chat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/chat/chattest"
)

const fakeCodexVar = "CMDR_FAKE_CODEX"

// maybeRunFakeCodex plays `codex app-server` when the bridge runs the test
// binary as its Codex: it records its argv and every request it reads.
func maybeRunFakeCodex() {
	if os.Getenv(fakeCodexVar) != "1" || len(os.Args) < 2 || os.Args[1] != "app-server" {
		return
	}
	dir := os.Getenv(chattest.DirVar)
	if os.Getenv(fakeCodexHoldVar) == "1" {
		fakeCodexHolding(dir)
	}
	argv, _ := json.Marshal(os.Args[1:])
	_ = os.WriteFile(filepath.Join(dir, "codex-argv.json"), argv, 0o600)
	f, _ := os.OpenFile(filepath.Join(dir, "codex-requests.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	fakeCodex(io.TeeReader(os.Stdin, f), os.Stdout, nil)
	os.Exit(0)
}

// codexHarness is a harness whose roles resolve their runtime like the real
// resolver, with Codex played by maybeRunFakeCodex.
func codexHarness(t *testing.T) *harness {
	h := newHarness(t)
	t.Setenv(fakeCodexVar, "1")
	t.Setenv("COMMANDER_CODEX_BIN", os.Args[0])
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	_ = os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte("[mcp_servers.mcp_agent_mail]\nurl = \"http://127.0.0.1:1/mcp/\"\n"), 0o600)
	base := h.opts.Resolver
	h.opts.Resolver = ResolverFunc(func(ctx context.Context, role string) (Spec, error) {
		spec, err := base.Resolve(ctx, role)
		if err != nil {
			return spec, err
		}
		return WithRuntime(spec, h.home), nil
	})
	h.e = New(h.opts)
	return h
}

func (h *harness) codexRequests(method string) []string {
	h.t.Helper()
	raw, _ := os.ReadFile(filepath.Join(h.fakeDir, "codex-requests.jsonl"))
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		var m struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal([]byte(l), &m) == nil && m.Method == method {
			out = append(out, string(m.Params))
		}
	}
	return out
}

func TestSetRuntimeRestartsARunningChatOnCodexAndBack(t *testing.T) {
	h := codexHarness(t)
	h.role("operator-a")
	h.start("operator-a")
	sub := h.subscribe("operator-a")
	if err := h.e.Send(context.Background(), "operator-a", "hello"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, sub, isKind(KindResult))

	st, err := h.e.SetRuntime(context.Background(), "operator-a", RuntimeCodex, SwitchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running || st.Runtime != RuntimeCodex {
		t.Fatalf("after switch: %+v", st)
	}
	sub = h.subscribe("operator-a")
	if err := h.e.Send(context.Background(), "operator-a", "are you there"); err != nil {
		t.Fatal(err)
	}
	seen := waitFor(t, sub, isKind(KindResult))
	if !strings.Contains(summarize(seen), "7 is prime") {
		t.Fatalf("codex reply missing: %s", summarize(seen))
	}
	st, _ = h.e.Status("operator-a")
	if !strings.HasPrefix(st.Model, "codex:") || st.CLIVersion != "codex-0.163.0" {
		t.Errorf("status model/version = %q %q", st.Model, st.CLIVersion)
	}
	// The first Codex turn reads the Claude conversation it takes over.
	turns := h.codexRequests("turn/start")
	if len(turns) != 1 || !strings.Contains(turns[0], filepath.Join(h.home, "chat", "operator-a.jsonl")) {
		t.Fatalf("first codex turn has no handoff: %v", turns)
	}
	// Codex never loads the agent-mail MCP server.
	argv, _ := os.ReadFile(filepath.Join(h.fakeDir, "codex-argv.json"))
	if !strings.Contains(string(argv), "mcp_servers.mcp_agent_mail.enabled=false") {
		t.Errorf("app-server argv = %s", argv)
	}

	st, err = h.e.SetRuntime(context.Background(), "operator-a", RuntimeClaude, SwitchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running || st.Runtime != RuntimeClaude {
		t.Fatalf("back to claude: %+v", st)
	}
	sub = h.subscribe("operator-a")
	if err := h.e.Send(context.Background(), "operator-a", "and now"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, sub, isKind(KindResult))
	if _, err := os.Stat(HandoffFile(h.home, "operator-a")); !os.IsNotExist(err) {
		t.Errorf("handoff marker not consumed: %v", err)
	}
}

func TestSetRuntimeOnAStoppedChatOnlyRecordsIt(t *testing.T) {
	h := codexHarness(t)
	h.role("operator-b")
	st, err := h.e.SetRuntime(context.Background(), "operator-b", RuntimeCodex, SwitchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Running || st.Runtime != RuntimeCodex || ReadRuntime(h.home, "operator-b") != RuntimeCodex {
		t.Fatalf("status = %+v", st)
	}
	if _, err := h.e.SetRuntime(context.Background(), "operator-b", "gemini", SwitchOptions{}); err == nil {
		t.Fatal("unknown runtime accepted")
	}
}

func TestSetRuntimeRefusesMidTurn(t *testing.T) {
	h := codexHarness(t)
	h.role("operator-c")
	h.start("operator-c")
	sub := h.subscribe("operator-c")
	if err := h.e.Send(context.Background(), "operator-c", "long"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, sub, isKind(KindTextDelta))
	if _, err := h.e.SetRuntime(context.Background(), "operator-c", RuntimeCodex, SwitchOptions{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("mid-turn switch: %v", err)
	}
	if ReadRuntime(h.home, "operator-c") != RuntimeClaude {
		t.Fatal("a refused switch must not record the runtime")
	}
}

// Mail reaches a Codex chat the way it reaches a native Claude chat: the wake
// loop's DeliverMail, between turns (there is no Stop hook to deliver mid-turn).
func TestDeliverMailReachesACodexChat(t *testing.T) {
	h := codexHarness(t)
	h.role("operator-d")
	if err := WriteRuntime(h.home, "operator-d", RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	h.start("operator-d")
	sub := h.subscribe("operator-d")
	if err := h.e.DeliverMail(context.Background(), "operator-d", "[mail] manager-x: review this"); err != nil {
		t.Fatal(err)
	}
	// The start's own events (session, state) may reach the subscription
	// before the mail does; skip them. The first user event, ahead of the
	// turn's result, must be the mail. waitFor bounds the wait.
	seen := waitFor(t, sub, isKind(KindResult))
	var user *Event
	for i := range seen {
		if seen[i].Kind == KindUser {
			user = &seen[i]
			break
		}
	}
	if user == nil || user.Origin != "mail" || !strings.Contains(user.Text, "review this") {
		t.Fatalf("first user event before the result = %+v; saw %s", user, summarize(seen))
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(h.codexRequests("turn/start")) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if turns := h.codexRequests("turn/start"); len(turns) != 1 || !strings.Contains(turns[0], "review this") {
		t.Fatalf("mail did not start a codex turn: %v", turns)
	}
}

func TestWriteRuntimeSetsUpTheHandoff(t *testing.T) {
	home := t.TempDir()
	role := "manager-x"
	os.MkdirAll(filepath.Join(home, "chat"), 0o700)
	os.WriteFile(filepath.Join(home, "chat", role+".jsonl"), []byte("{}\n"), 0o600)
	os.WriteFile(CodexThreadFile(home, role), []byte("th-old\n"), 0o600)

	if err := WriteRuntime(home, role, RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	if fileExists(CodexThreadFile(home, role)) {
		t.Error("switching to codex must start a fresh thread")
	}
	if spec := WithRuntime(Spec{Role: role, Model: "opus"}, home); spec.Runtime != RuntimeCodex || spec.Model != DefaultCodexModel || spec.Handoff == "" {
		t.Errorf("codex spec = %+v", spec)
	}
	if err := WriteRuntime(home, role, RuntimeClaude); err != nil {
		t.Fatal(err)
	}
	if spec := WithRuntime(Spec{Role: role, Model: "opus"}, home); spec.Runtime != RuntimeClaude || spec.Model != "opus" || spec.Handoff == "" {
		t.Errorf("claude spec after codex = %+v", spec)
	}
	if err := WriteRuntime(home, "bad role", RuntimeCodex); err == nil {
		t.Error("invalid role accepted")
	}
}

func TestCodexAppServerArgsDisableOnlyConfiguredAgentMail(t *testing.T) {
	dir := t.TempDir()
	if got, err := CodexMCPLaunchArgs([]string{filepath.Join(dir, "config.toml")}, ""); err != nil || len(got) != 1 {
		t.Errorf("no config: %v", got)
	}
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[mcp_servers.mcp_agent_mail]\nurl = \"x\"\n[mcp_servers.other]\ncommand = \"y\"\n"), 0o600)
	args, err := CodexMCPLaunchArgs([]string{filepath.Join(dir, "config.toml")}, "")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(args, " ")
	if got != "app-server -c mcp_servers.mcp_agent_mail.enabled=false" {
		t.Errorf("args = %q", got)
	}
}

func TestCodexModel(t *testing.T) {
	// No model, a Claude one or another provider: Commander's Codex model, never the Codex app's own default.
	for in, want := range map[string]string{"": DefaultCodexModel, "anthropic/claude-sonnet-4-5": DefaultCodexModel, "opus": DefaultCodexModel, "openai/gpt-5.5": "gpt-5.5", "gpt-5.5": "gpt-5.5"} {
		if got := CodexModel(in); got != want {
			t.Errorf("CodexModel(%q) = %q, want %q", in, got, want)
		}
	}
}

// A drawn Codex role whose bridge host survives a cmdrd restart is adopted as
// Codex even when the share is now 0: the drop to Claude (and its handoff)
// waits for a start that spawns a new process.
// A host that holds the session while its dial fails (a hello deadline under load) is
// still running Codex: the failed start leaves the role, its handoff and that host alone.
func TestDrawnCodexIsKeptWhenAnUnreachableHostHoldsTheLock(t *testing.T) {
	h := codexHarness(t)
	h.role("operator-e")
	t.Setenv(agentrt.CodexManagerShareEnvVar, "0")
	if err := WriteAutoRuntime(h.home, "operator-e", RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	spec, err := h.opts.Resolver.Resolve(context.Background(), "operator-e")
	if err != nil {
		t.Fatal(err)
	}
	host, err := acquireSessionLock(h.e.lockDir(), spec.SessionID, "operator-e")
	if err != nil {
		t.Fatal(err)
	}
	var owned *OwnedError
	if _, err := h.e.Start(context.Background(), "operator-e", StartOptions{}); !errors.As(err, &owned) {
		t.Fatalf("start beside the host: %v", err)
	}
	if ReadRuntime(h.home, "operator-e") != RuntimeCodex || fileExists(HandoffFile(h.home, "operator-e")) {
		t.Fatal("a failed start relabelled the role or wrote a handoff")
	}

	host.release()
	if st := h.start("operator-e"); st.Runtime != RuntimeClaude {
		t.Fatalf("a new process at share 0 runs on %s", st.Runtime)
	}
}

func TestDrawnCodexHostIsAdoptedAsCodexAtShareZero(t *testing.T) {
	h := codexHarness(t)
	h.role("operator-e")
	t.Setenv(agentrt.CodexManagerShareEnvVar, "0.5")
	if err := WriteAutoRuntime(h.home, "operator-e", RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	if st := h.start("operator-e"); st.Runtime != RuntimeCodex {
		t.Fatalf("started as %s", st.Runtime)
	}

	t.Setenv(agentrt.CodexManagerShareEnvVar, "0")
	e2 := h.restart()
	waitUntil(t, "adopted", func() bool { return e2.Running("operator-e") })
	if st, _ := e2.Status("operator-e"); st.Runtime != RuntimeCodex {
		t.Fatalf("adopted Codex host reported as %s", st.Runtime)
	}
	if ReadRuntime(h.home, "operator-e") != RuntimeCodex || fileExists(HandoffFile(h.home, "operator-e")) {
		t.Fatal("adoption relabelled the role or wrote a handoff")
	}

	if err := e2.Stop(context.Background(), "operator-e"); err != nil {
		t.Fatal(err)
	}
	if st := h.start("operator-e"); st.Runtime != RuntimeClaude {
		t.Fatalf("a new process at share 0 runs on %s", st.Runtime)
	}
}

func TestEffectiveRuntimeIdempotencePreservesThreadAndIntent(t *testing.T) {
	home := t.TempDir()
	role := "operator-repeat"
	if err := WriteRuntime(home, role, RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	thread := CodexThreadFile(home, role)
	if err := os.WriteFile(thread, []byte("sample-thread"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := WriteEffectiveRuntime(home, role, RuntimeCodex); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(thread)
	if err != nil || string(b) != "sample-thread" || ReadRuntimePin(home, role) != RuntimeCodex {
		t.Fatalf("repeat changed thread/intent: %q %v", b, err)
	}
}
