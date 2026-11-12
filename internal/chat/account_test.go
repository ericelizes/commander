package chat

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/claudeacct"
)

// accountHarness is a harness whose roles resolve their account like the real
// resolver, with a fake HOME and Keychain: "default" and every account in
// loggedIn count as logged in.
func accountHarness(t *testing.T, loggedIn ...string) *harness {
	h := codexHarness(t)
	user := t.TempDir()
	t.Setenv("HOME", user)
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	os.MkdirAll(filepath.Join(user, ".claude", "skills"), 0o755)
	held := map[string]bool{claudeacct.KeychainService(""): true}
	for _, name := range loggedIn {
		if _, err := claudeacct.Create(h.home, name); err != nil {
			t.Fatal(err)
		}
		held[claudeacct.KeychainService(claudeacct.Dir(h.home, name))] = true
	}
	prev := claudeacct.KeychainHas
	claudeacct.KeychainHas = func(s string) bool { return held[s] }
	claudeacct.ForgetProbes()
	t.Cleanup(func() { claudeacct.KeychainHas = prev; claudeacct.ForgetProbes() })
	base := h.opts.Resolver
	h.opts.Resolver = ResolverFunc(func(ctx context.Context, role string) (Spec, error) {
		spec, err := base.Resolve(ctx, role)
		return WithAccount(spec, h.home), err
	})
	h.e = New(h.opts)
	return h
}

func TestSetAccountResumesTheSameSessionOnTheOtherAccount(t *testing.T) {
	h := accountHarness(t, "work")
	id := h.role("manager-a")
	h.start("manager-a")
	sub := h.subscribe("manager-a")
	if err := h.e.Send(context.Background(), "manager-a", "remember 42"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, sub, isKind(KindResult))

	st, err := h.e.SetAccount(context.Background(), "manager-a", "work", SwitchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running || st.Account != "work" || ReadAccount(h.home, "manager-a") != "work" {
		t.Fatalf("after switch: %+v", st)
	}
	workDir := claudeacct.Dir(h.home, "work")
	// The restarted claude writes its record a moment after Start returns: wait
	// for it, or the first run's record (no account, not resumed) is read.
	rec := h.fakeRecord("argv-" + id)
	for deadline := time.Now().Add(5 * time.Second); rec["config_dir"] != workDir && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		rec = h.fakeRecord("argv-" + id)
	}
	if rec["config_dir"] != workDir {
		t.Errorf("claude ran with CLAUDE_CONFIG_DIR=%v, want %s", rec["config_dir"], workDir)
	}
	if argv := strings.Join(toStrings(rec["argv"]), " "); !strings.Contains(argv, "--resume "+id) {
		t.Errorf("not resumed: %s", argv)
	}
	copied := FindTranscript(filepath.Join(workDir, "projects"), id)
	if copied == "" {
		t.Fatal("transcript not carried into the account")
	}
	if raw, _ := os.ReadFile(copied); !strings.Contains(string(raw), "remember 42") {
		t.Errorf("carried transcript lacks the conversation: %s", raw)
	}
	sub = h.subscribe("manager-a")
	if err := h.e.Send(context.Background(), "manager-a", "still there"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, sub, isKind(KindResult))
	// The fake claude appends to its transcript just after the result: wait for
	// it, as a real turn would be on disk before anyone switches.
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if raw, _ := os.ReadFile(FindTranscript(filepath.Join(workDir, "projects"), id)); strings.Contains(string(raw), "still there") {
			break
		}
	}

	// And back: the default account gets the newer transcript.
	if _, err := h.e.SetAccount(context.Background(), "manager-a", claudeacct.Default, SwitchOptions{}); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(FindTranscript(h.projects, id)); !strings.Contains(string(raw), "still there") {
		t.Errorf("default transcript not refreshed: %s", raw)
	}
	rec = h.fakeRecord("argv-" + id)
	for deadline := time.Now().Add(5 * time.Second); rec["config_dir"] != "" && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		rec = h.fakeRecord("argv-" + id)
	}
	if rec["config_dir"] != "" {
		t.Errorf("default account must not set CLAUDE_CONFIG_DIR: %v", rec["config_dir"])
	}
}

func TestSetAccountRefusesMidTurnAndLoggedOutAccounts(t *testing.T) {
	h := accountHarness(t, "work")
	if _, err := claudeacct.Create(h.home, "spare"); err != nil {
		t.Fatal(err)
	}
	h.role("manager-b")
	h.start("manager-b")
	sub := h.subscribe("manager-b")
	if err := h.e.Send(context.Background(), "manager-b", "long"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, sub, isKind(KindTextDelta))
	if _, err := h.e.SetAccount(context.Background(), "manager-b", "work", SwitchOptions{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("mid-turn switch: %v", err)
	}
	if ReadAccount(h.home, "manager-b") != claudeacct.Default {
		t.Fatal("a refused switch must not record the account")
	}
	if _, err := h.e.SetAccount(context.Background(), "manager-b", "spare", SwitchOptions{}); err == nil || !strings.Contains(err.Error(), "claude auth login") {
		t.Fatalf("logged-out account: %v", err)
	}
	if _, err := h.e.SetAccount(context.Background(), "manager-b", "nope", SwitchOptions{}); err == nil {
		t.Fatal("unknown account accepted")
	}
}

func TestSetAccountFromCodexRunsClaudeOnThatAccount(t *testing.T) {
	h := accountHarness(t, "work")
	h.role("manager-c")
	if err := WriteRuntime(h.home, "manager-c", RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	h.start("manager-c")
	st, err := h.e.SetAccount(context.Background(), "manager-c", "work", SwitchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ReadRuntimePin(h.home, "manager-c") != RuntimeCodex {
		t.Fatal("account switch overwrote explicit runtime intent")
	}
	if st.Runtime != RuntimeClaude || st.Account != "work" {
		t.Fatalf("status = %+v", st)
	}
}

func TestUsageLimitShowsANoticeAndContinueSwitchesAccount(t *testing.T) {
	h := accountHarness(t, "work")
	h.role("operator-d")
	h.start("operator-d")
	sub := h.subscribe("operator-d")
	if err := h.e.Send(context.Background(), "operator-d", "usagelimit"); err != nil {
		t.Fatal(err)
	}
	seen := waitFor(t, sub, func(e Event) bool { return e.Kind == KindNotice && e.Subtype == "usage_limit" })
	if n := seen[len(seen)-1]; !strings.Contains(n.Text, "default") || !strings.Contains(n.Text, "You've hit your limit") {
		t.Errorf("notice = %q", n.Text)
	}
	st, _ := h.e.Status("operator-d")
	if st.UsageLimit == nil || st.UsageLimit.Account != "default" || st.UsageLimit.ResetsAt != 4102444800 {
		t.Fatalf("status usage limit = %+v", st.UsageLimit)
	}
	if u, _ := h.e.Usage().Get(UsageKey(RuntimeClaude, "default")); u.Status != UsageOut || u.FiveHour == nil || u.FiveHour.Utilization != 1 {
		t.Errorf("usage = %+v", u)
	}
	if got := h.e.NextAccount("default"); got != "work" {
		t.Errorf("next account = %q", got)
	}

	// The notice's one click: switch and continue.
	sub = h.subscribe("operator-d")
	st, err := h.e.SetAccount(context.Background(), "operator-d", "work", SwitchOptions{Continue: true})
	if err != nil {
		t.Fatal(err)
	}
	if st.UsageLimit != nil {
		t.Error("the limit stays after switching")
	}
	seen = waitFor(t, sub, isKind(KindResult))
	var cont bool
	for _, e := range seen {
		cont = cont || (e.Kind == KindUser && e.Text == ContinueMessage)
	}
	if !cont {
		t.Fatalf("no continue message: %s", summarize(seen))
	}
}

func TestAutoSwitchMovesToTheNextAccountWithUsageLeft(t *testing.T) {
	h := accountHarness(t, "work", "zeta")
	// zeta ran out earlier and has not reset: auto-switch must skip it.
	h.e.Usage().MarkOut(RuntimeClaude, "zeta", "manager-x", time.Now().Add(time.Hour).Unix(), time.Now())
	h.role("manager-e")
	if err := SetAutoSwitch(h.home, "manager-e", true); err != nil {
		t.Fatal(err)
	}
	if err := WriteAccount(h.home, "manager-e", "work"); err != nil {
		t.Fatal(err)
	}
	h.start("manager-e")
	sub := h.subscribe("manager-e")
	if err := h.e.Send(context.Background(), "manager-e", "usagelimit"); err != nil {
		t.Fatal(err)
	}
	seen := waitFor(t, sub, func(e Event) bool { return e.Kind == KindNotice && e.Subtype == "usage_limit_switch" })
	if n := seen[len(seen)-1]; !strings.Contains(n.Text, "from Claude account work (out of usage) to default") {
		t.Fatalf("switch notice = %q", n.Text)
	}
	if ReadAccount(h.home, "manager-e") != claudeacct.Default {
		t.Error("account not switched")
	}
}

func TestUsageStoreMergesSparseReportsAndPersists(t *testing.T) {
	home := t.TempDir()
	s := OpenUsage(home)
	now := time.Unix(1792690000, 0)
	s.Record(RuntimeClaude, "", "commander", json.RawMessage(`{"status":"allowed_warning","rateLimitType":"five_hour","utilization":0.99,"resetsAt":1792699200,
		"unifiedWindows":{"five_hour":{"utilization":0.99,"resetsAt":1792699200},"seven_day":{"utilization":0.39,"resetsAt":1793203200}},"overageStatus":"rejected"}`), now)
	s.Record(RuntimeClaude, "", "manager-x", json.RawMessage(`{"status":"rejected","rateLimitType":"five_hour","resetsAt":1792699200,"overageStatus":"rejected"}`), now.Add(time.Minute))
	u, _ := OpenUsage(home).Get("claude:default") // reloaded from disk
	if u.Status != UsageOut || u.OutUntil != 1792699200 || u.Role != "manager-x" || u.SevenDay == nil || u.SevenDay.Utilization != 0.39 {
		t.Fatalf("usage = %+v", u)
	}
	if !u.Out(now) || u.Out(time.Unix(1792699200, 0)) {
		t.Error("out must last until the reset")
	}
	if c := u.Current(time.Unix(1792699300, 0)); c.FiveHour.Utilization != 0 || c.SevenDay.Utilization != 0.39 {
		t.Errorf("after the 5h reset: %+v %+v", c.FiveHour, c.SevenDay)
	}
	// Overage taking over is not "out".
	s.Record(RuntimeClaude, "work", "manager-y", json.RawMessage(`{"status":"rejected","overageStatus":"allowed","isUsingOverage":true}`), now)
	if u, _ := s.Get("claude:work"); u.Status != UsageOK {
		t.Errorf("overage = %+v", u)
	}
}

func TestCodexRateLimitsBecomeARateLimitLine(t *testing.T) {
	info := codexRateLimitInfo(json.RawMessage(`{"rateLimits":{"primary":{"usedPercent":85,"windowDurationMins":300,"resetsAt":1792699200},
		"secondary":{"usedPercent":30,"windowDurationMins":10080,"resetsAt":1793203200},"planType":"pro"}}`))
	raw, _ := json.Marshal(info)
	s := OpenUsage(t.TempDir())
	s.Record(RuntimeCodex, "", "operator-x", raw, time.Unix(1792690000, 0))
	u, _ := s.Get("codex")
	if u.Status != UsageWarning || u.FiveHour.Utilization != 0.85 || u.SevenDay.ResetsAt != 1793203200 || u.Account != "" {
		t.Fatalf("codex usage = %+v (%s)", u, raw)
	}
	raw, _ = json.Marshal(codexRateLimitInfo(json.RawMessage(`{"rateLimits":{"primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":1792699200},"rateLimitReachedType":"rate_limit_reached"}}`)))
	s.Record(RuntimeCodex, "", "operator-x", raw, time.Unix(1792690100, 0))
	if u, _ := s.Get("codex"); u.Status != UsageOut || u.OutUntil != 1792699200 || u.SevenDay == nil {
		t.Fatalf("codex out = %+v", u)
	}
	if codexRateLimitInfo(json.RawMessage(`{"rateLimits":{}}`)) != nil {
		t.Error("an empty update must not report")
	}
}

func TestUsageLimitOfReadsBothSignals(t *testing.T) {
	for _, tc := range []struct {
		ev   Event
		want bool
	}{
		{Event{Kind: KindRateLimit, RateLimit: json.RawMessage(`{"status":"rejected","overageStatus":"rejected"}`)}, true},
		{Event{Kind: KindRateLimit, RateLimit: json.RawMessage(`{"status":"allowed_warning"}`)}, false},
		{Event{Kind: KindRateLimit, RateLimit: json.RawMessage(`{"status":"rejected","isUsingOverage":true}`)}, false},
		{Event{Kind: KindText, Error: "rate_limit", Text: "You've hit your limit · resets 3pm"}, true},
		{Event{Kind: KindText, Error: "billing_error", Text: "You've hit your monthly spend limit"}, true},
		{Event{Kind: KindText, Error: "server_error", Text: "API Error: 529 Overloaded"}, false},
		{Event{Kind: KindText, Text: "I hit your usage limit joke"}, false},
	} {
		if got := usageLimitOf(tc.ev) != nil; got != tc.want {
			t.Errorf("%+v: %v", tc.ev, got)
		}
	}
}

// Every Manager start goes through the steer: a fresh Manager whose default
// account is out starts on the next account with usage, keeps default as its
// intent, and the move is noticed and reported; once default has usage again
// the next start goes back to it.
func TestManagerStartMovesOffAnOutAccountAndBack(t *testing.T) {
	h := accountHarness(t, "work")
	moved := make(chan string, 4)
	h.e.SetUsageMoveHook(func(role, text string) { moved <- role + ": " + text })
	h.e.Usage().MarkOut(RuntimeClaude, claudeacct.Default, "manager-x", time.Now().Add(time.Hour).Unix(), time.Now())
	h.role("manager-n")
	st := h.start("manager-n")
	if st.Account != "work" || ReadAccount(h.home, "manager-n") != "work" {
		t.Fatalf("started on %q (file %q)", st.Account, ReadAccount(h.home, "manager-n"))
	}
	if ReadAccountIntent(h.home, "manager-n") != claudeacct.Default {
		t.Error("intent not kept")
	}
	select {
	case m := <-moved:
		if !strings.Contains(m, "manager-n") || !strings.Contains(m, "default (out of usage) to work") {
			t.Fatalf("note %q", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no move note")
	}
	if err := h.e.Stop(context.Background(), "manager-n"); err != nil {
		t.Fatal(err)
	}
	h.e.Usage().Record(RuntimeClaude, "", "manager-x", json.RawMessage(`{"status":"allowed"}`), time.Now())
	if st := h.start("manager-n"); st.Account != claudeacct.Default {
		t.Fatalf("not back on default: %q", st.Account)
	}
	if ReadAccountIntent(h.home, "manager-n") != "" {
		t.Error("intent left after going back")
	}
}

// A person's account choice is the intent: a start while it is out runs
// elsewhere without forgetting it.
func TestManagerStartKeepsAPersonsChoice(t *testing.T) {
	h := accountHarness(t, "work")
	if err := WriteAccount(h.home, "manager-p", "work"); err != nil {
		t.Fatal(err)
	}
	h.e.Usage().MarkOut(RuntimeClaude, "work", "manager-x", time.Now().Add(time.Hour).Unix(), time.Now())
	h.role("manager-p")
	if st := h.start("manager-p"); st.Account != claudeacct.Default {
		t.Fatalf("account %q", st.Account)
	}
	if ReadAccountIntent(h.home, "manager-p") != "work" {
		t.Error("the person's choice was dropped")
	}
}

// Nothing with usage left: the start is refused, visibly, and nothing moves.
func TestManagerStartRefusedWhenEveryAccountIsOut(t *testing.T) {
	h := accountHarness(t, "work")
	h.opts.CodexHooksReady = func() bool { return false }
	h.e = New(h.opts)
	for _, a := range []string{claudeacct.Default, "work"} {
		h.e.Usage().MarkOut(RuntimeClaude, a, "manager-x", time.Now().Add(time.Hour).Unix(), time.Now())
	}
	h.role("manager-z")
	_, err := h.e.Start(context.Background(), "manager-z", StartOptions{})
	if !errors.Is(err, ErrNoUsage) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(h.e.CapacityFailures()["manager-z"], ErrNoUsage.Error()) {
		t.Fatal("refused start missing from capacity state")
	}
	copy := h.e.CapacityFailures()
	delete(copy, "manager-z")
	if len(h.e.CapacityFailures()) != 1 {
		t.Fatal("capacity state is not a copy")
	}
	if _, err := os.Stat(AccountFile(h.home, "manager-z")); !os.IsNotExist(err) {
		t.Error("account written")
	}
	h.e.Usage().Record(RuntimeClaude, "", "manager-x", json.RawMessage(`{"status":"allowed"}`), time.Now())
	h.start("manager-z")
	if len(h.e.CapacityFailures()) != 0 {
		t.Fatal("successful start did not clear capacity")
	}
}

// A Manager (no auto-switch file) that hits a usage limit moves to the next
// account with usage, keeps its intent, is told to re-read its handover, and
// the move is reported.
func TestManagerMovesAtUsageLimitAndReportsIt(t *testing.T) {
	h := accountHarness(t, "work")
	moved := make(chan string, 1)
	h.e.SetUsageMoveHook(func(role, text string) { moved <- role + ": " + text })
	h.role("manager-m")
	h.start("manager-m")
	sub := h.subscribe("manager-m")
	if err := h.e.Send(context.Background(), "manager-m", "usagelimit"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, sub, func(e Event) bool { return e.Kind == KindNotice && e.Subtype == "usage_limit_switch" })
	if got := ReadAccount(h.home, "manager-m"); got != "work" {
		t.Fatalf("account %q", got)
	}
	if ReadAccountIntent(h.home, "manager-m") != claudeacct.Default {
		t.Error("the auto-move overwrote the account choice")
	}
	waitFor(t, sub, func(e Event) bool {
		return e.Kind == KindUser && strings.Contains(e.Text, "re-read your handover") && strings.Contains(e.Text, "--agent manager-m")
	})
	select {
	case m := <-moved:
		if !strings.Contains(m, "manager-m") || !strings.Contains(m, "to work") {
			t.Fatalf("note %q", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no move note")
	}
}

// An unpinned Manager on Codex at Codex's limit moves to Claude, and onto a
// Claude account with usage (never an out one); a pinned one moves too (d82)
// with its Codex pin kept, and goes back to Codex once Codex has usage again.
func TestManagerLeavesAnOutCodex(t *testing.T) {
	h := accountHarness(t, "work")
	h.e.opts.CodexHooksReady = func() bool { return true }
	t.Setenv(agentrt.CodexManagerShareEnvVar, "1") // keep the drawn Codex run at its start
	h.e.Usage().MarkOut(RuntimeClaude, claudeacct.Default, "manager-x", time.Now().Add(time.Hour).Unix(), time.Now())
	for _, role := range []string{"manager-c", "manager-pc"} {
		h.role(role)
	}
	if err := WriteAutoRuntime(h.home, "manager-c", RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	if err := WriteRuntime(h.home, "manager-pc", RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	// Each Codex start reads its rate limits (allowed); mark Codex out only after.
	for _, role := range []string{"manager-pc", "manager-c"} {
		h.start(role)
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			if u, ok := h.e.Usage().Get(UsageKey(RuntimeCodex, "")); ok && u.Role == role {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: no Codex usage read", role)
			}
		}
	}
	h.e.Usage().MarkOut(RuntimeCodex, "", "manager-c", time.Now().Add(time.Hour).Unix(), time.Now())

	sub := h.subscribe("manager-c")
	c, _ := h.e.chat("manager-c")
	h.e.moveManager(c)
	seen := waitFor(t, sub, func(e Event) bool { return e.Kind == KindNotice && e.Subtype == "usage_limit_switch" })
	if n := seen[len(seen)-1]; !strings.Contains(n.Text, "from Codex (out of usage) to Claude account work") {
		t.Fatalf("notice %q", n.Text)
	}
	st, _ := h.e.Status("manager-c")
	if st.Runtime != RuntimeClaude || st.Account != "work" || RuntimePinned(h.home, "manager-c") {
		t.Fatalf("after the move: %+v pinned=%v", st, RuntimePinned(h.home, "manager-c"))
	}

	sub = h.subscribe("manager-pc")
	pc, _ := h.e.chat("manager-pc")
	h.e.moveManager(pc)
	seen = waitFor(t, sub, func(e Event) bool { return e.Kind == KindNotice && e.Subtype == "usage_limit_switch" })
	if n := seen[len(seen)-1]; !strings.Contains(n.Text, "to Claude account work") || !strings.Contains(n.Text, "Codex pin stays") {
		t.Fatalf("pinned notice %q", n.Text)
	}
	if st, _ := h.e.Status("manager-pc"); st.Runtime != RuntimeClaude || st.Account != "work" {
		t.Fatalf("pinned Codex Manager left on an out Codex: %+v", st)
	}
	if !RuntimePinned(h.home, "manager-pc") || ManagerPin(h.home, "manager-pc") != RuntimeCodex {
		t.Fatalf("the fallback dropped the pin: pinned=%v pin=%q", RuntimePinned(h.home, "manager-pc"), ManagerPin(h.home, "manager-pc"))
	}

	// Codex has usage again: the next start goes back to the pinned runtime.
	h.e.Usage().MarkOut(RuntimeCodex, "", "manager-c", time.Now().Add(-time.Minute).Unix(), time.Now())
	if _, err := h.e.restart(context.Background(), "manager-pc", SwitchOptions{}); err != nil {
		t.Fatal(err)
	}
	if st, _ := h.e.Status("manager-pc"); st.Runtime != RuntimeCodex {
		t.Fatalf("pinned Codex Manager not back on Codex: %+v", st)
	}
	if ManagerPin(h.home, "manager-pc") != RuntimeCodex || fileExists(RuntimeFallbackFile(h.home, "manager-pc")) {
		t.Fatal("back on its pin, the fallback marker must go")
	}
}

// d82 through the engine: a Manager whose account a person chose, with every
// Claude account out, starts on Codex; a drawn Codex Manager at share 0 with
// Claude out restarts on Codex with no move and its thread kept.
func TestManagerAccountChoiceStillFallsBackToCodex(t *testing.T) {
	h := accountHarness(t, "work")
	h.e.opts.CodexHooksReady = func() bool { return true }
	ctx := context.Background()

	// A Manager drawn onto Codex runs once, then the share drops to 0.
	t.Setenv(agentrt.CodexManagerShareEnvVar, "1")
	h.role("manager-d")
	if err := WriteAutoRuntime(h.home, "manager-d", RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	h.start("manager-d")
	if err := h.e.Stop(ctx, "manager-d"); err != nil {
		t.Fatal(err)
	}
	t.Setenv(agentrt.CodexManagerShareEnvVar, "0")

	h.role("manager-f")
	if _, err := h.e.SetAccount(ctx, "manager-f", "work", SwitchOptions{}); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Hour).Unix()
	h.e.Usage().MarkOut(RuntimeClaude, claudeacct.Default, "manager-x", until, time.Now())
	h.e.Usage().MarkOut(RuntimeClaude, "work", "manager-x", until, time.Now())
	if st := h.start("manager-f"); st.Runtime != RuntimeCodex {
		t.Fatalf("account choice blocked the Codex fallback: %+v", st)
	}

	// Every Claude account out: it stays on Codex. The bug flipped it to Claude
	// and back at the start (thread file deleted) and posted a false
	// "moved from Claude account ... to Codex" notice.
	if st := h.start("manager-d"); st.Runtime != RuntimeCodex {
		t.Fatalf("drawn Codex at share 0 with Claude out: %+v", st)
	}
	if b, _ := os.ReadFile(filepath.Join(h.e.logDir(), "manager-d.jsonl")); strings.Contains(string(b), "usage_limit_switch") {
		t.Fatalf("a drawn Codex Manager that never left Codex was reported moved: %s", b)
	}
}

// A refused move leaves the current process alive. Its next successful turn
// proves recovery without any new start/plan; another refusal must stay visible.
func TestCapacityFailureClearsOnSameProcessRecovery(t *testing.T) {
	h := accountHarness(t)
	h.opts.CodexHooksReady = func() bool { return false }
	h.e = New(h.opts)
	h.role("manager-recovery")
	before := h.start("manager-recovery")
	sub := h.subscribe("manager-recovery")
	if err := h.e.Send(context.Background(), "manager-recovery", "usagelimit"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, sub, func(ev Event) bool { return ev.Kind == KindNotice && strings.Contains(ev.Text, "Not moved:") })
	if len(h.e.CapacityFailures()) != 1 {
		t.Fatal("refused move not retained")
	}
	if err := h.e.Send(context.Background(), "manager-recovery", "usagelimit"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, sub, func(ev Event) bool { return ev.Kind == KindNotice && strings.Contains(ev.Text, "Not moved:") })
	if len(h.e.CapacityFailures()) != 1 {
		t.Fatal("failed turn cleared capacity")
	}
	if err := h.e.Send(context.Background(), "manager-recovery", "recovered"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, sub, isKind(KindResult))
	// Result publication precedes settling its capacity state.
	deadline := time.Now().Add(5 * time.Second)
	for len(h.e.CapacityFailures()) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(h.e.CapacityFailures()) != 0 {
		t.Fatal("successful same-process result left capacity refusal")
	}
	after, err := h.e.Status("manager-recovery")
	if err != nil {
		t.Fatal(err)
	}
	if before.PID != after.PID || !after.Running {
		t.Fatalf("process changed: before %+v after %+v", before, after)
	}
}

func TestCapacityFailureRequiresPositiveCurrentProcessEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result *Result
		stale  bool
	}{
		{name: "missing result"},
		{name: "failed result", result: &Result{Subtype: "success", IsError: true}},
		{name: "unknown result", result: &Result{}},
		{name: "old process", result: &Result{Subtype: "success"}, stale: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := New(Options{Home: t.TempDir()})
			p := &process{}
			c := &chatRole{role: "manager-proof", proc: p}
			if tc.stale {
				c.proc = &process{}
			}
			e.recordCapacity(c.role, ErrNoUsage)
			e.settleLimit(c, p, Event{Kind: KindResult, Result: tc.result})
			if len(e.CapacityFailures()) != 1 {
				t.Fatal("non-recovery evidence cleared refusal")
			}
		})
	}
}
