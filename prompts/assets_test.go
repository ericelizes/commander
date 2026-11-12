package promptassets

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/agentrt"
)

func TestEnsureCommanderAgent_WritesCommanderPrompt(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	if err := EnsureCommanderAgent(); err != nil {
		t.Fatalf("EnsureCommanderAgent: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(tmp, "opencode", "agents", "commander.md"))
	if err != nil {
		t.Fatalf("read commander.md: %v", err)
	}
	if !strings.Contains(string(body), "You are **Commander**") {
		t.Fatal("commander.md missing Commander prompt body")
	}
	if _, err := os.Stat(filepath.Join(tmp, "opencode", "agents", "chief.md")); !os.IsNotExist(err) {
		t.Fatalf("chief.md should not be written anymore; err=%v", err)
	}
}

func TestEnsureManagerAndOperatorAgents(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	if err := EnsureManagerAgent("manager-frontend"); err != nil {
		t.Fatalf("EnsureManagerAgent: %v", err)
	}
	if err := EnsureOperatorCodeAgent("operator-feat-auth"); err != nil {
		t.Fatalf("EnsureOperatorCodeAgent: %v", err)
	}
	if err := EnsureOperatorKnowledgeAgent("operator-4fc"); err != nil {
		t.Fatalf("EnsureOperatorKnowledgeAgent: %v", err)
	}

	for _, path := range []string{
		filepath.Join(tmp, "opencode", "agents", "manager-frontend.md"),
		filepath.Join(tmp, "opencode", "agents", "operator-feat-auth.md"),
		filepath.Join(tmp, "opencode", "agents", "operator-4fc.md"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(body), `model: "anthropic/claude-sonnet-4-5"`) {
			t.Fatalf("%s should not hardcode an invalid model", path)
		}
	}
}

func TestCommanderWorkspaceRoot_DefaultAndOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("COMMANDER_WORKSPACE_ROOT", "")

	root, err := CommanderWorkspaceRoot()
	if err != nil {
		t.Fatalf("default workspace root: %v", err)
	}
	wantDefault := filepath.Join(home, "code")
	if root != wantDefault {
		t.Fatalf("default root=%q want %q", root, wantDefault)
	}

	custom := filepath.Join(t.TempDir(), "workspace")
	t.Setenv("COMMANDER_WORKSPACE_ROOT", custom)
	root, err = CommanderWorkspaceRoot()
	if err != nil {
		t.Fatalf("custom workspace root: %v", err)
	}
	if root != custom {
		t.Fatalf("custom root=%q want %q", root, custom)
	}
}

func TestCommanderPromptContract(t *testing.T) {
	body, err := templateFS.ReadFile("commander.md")
	if err != nil {
		t.Fatalf("read commander prompt: %v", err)
	}
	text := string(body)
	for _, needle := range []string{
		"COMMANDER_WORKSPACE_ROOT",
		"using the exact path returned by the resolver",
		"cmdr internal-workstreams",
		"cmdr internal-open-workstream",
		"cmdr internal-resolve-add-project",
		"cmdr internal-create-objective",
		"from commander",
		"agent commander",
		"Do NOT do repo archaeology",
		"do NOT immediately ask for type/path",
		"bias toward delegation and organization",
		"I can create the objective now and we can fill in details later.",
		"Bias toward objective creation.",
		"Do NOT improvise with direct `bd create`, raw SQLite writes, or alternate bead shapes.",
		"Do not replace it with direct `bd` commands, direct SQLite writes, or a different data model.",
		"Do not speak to the user in raw shell commands unless they explicitly ask to see the command.",
		"Do not talk like a shell script.",
		"Only ask for extra information after state check + workspace search still leave the target unresolved.",
		"After a successful project add, stop after the concise success result.",
		"Do not turn a successful add into archaeology.",
		"Do not do broad startup exploration",
		"Acknowledge readiness in one short line.",
		"Manager session captured",
		"manager-<name>",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("commander prompt missing %q", needle)
		}
	}
	if strings.Contains(text, "from chief") || strings.Contains(text, "agent chief") {
		t.Fatal("commander prompt should not reference chief anymore")
	}
}

func TestEnsureClaudeAgent_WritesPromptBodyToCommanderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)

	if err := EnsureClaudeAgent(RoleManager, "manager-frontend"); err != nil {
		t.Fatalf("EnsureClaudeAgent: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(home, "claude-agents", "manager-frontend.md"))
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}
	got := string(body)
	if strings.HasPrefix(got, "---") || strings.Contains(got, `"mcp-agent-mail*": false`) {
		t.Errorf("opencode frontmatter must be stripped:\n%s", got[:200])
	}
	tmpl, _ := templateFS.ReadFile(RoleManager)
	contracts, _ := templateFS.ReadFile(ownershipPath)
	if !strings.Contains(string(tmpl), got[:200]) || !strings.HasSuffix(got, string(contracts)) {
		t.Errorf("a Manager prompt is the template body followed by the ownership contracts; got %d bytes", len(got))
	}
}

// Commander and Managers read one definition of "own this"; Operators do not get it.
func TestOwnershipContractsReachCommanderAndManagersOnly(t *testing.T) {
	for role, want := range map[string]bool{RoleCommander: true, RoleManager: true, operatorCodePath: false} {
		body, err := roleBody(role)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(body, "## Ownership contracts"); got != want {
			t.Errorf("%s: contracts included = %v, want %v", role, got, want)
		}
	}
}

func TestPushAndInstallPolicyInPrompts(t *testing.T) {
	for _, tc := range []struct {
		role    string
		needles []string
	}{
		{RoleCommander, []string{"without the user's OK", "internal-ship", "once per batch"}},
		{RoleManager, []string{"Push policy", "internal-ship", "Commander asks the user once per batch"}},
		{operatorCodePath, []string{"Git push policy", "--force-with-lease", "cmdr internal-ship", "Never install", "`hold` label", "stop shipping"}},
	} {
		body, err := roleBody(tc.role)
		if err != nil {
			t.Fatal(err)
		}
		for _, needle := range tc.needles {
			if !strings.Contains(body, needle) {
				t.Errorf("%s prompt missing %q", tc.role, needle)
			}
		}
	}
}

func TestMailMidRunInOperatorPrompt(t *testing.T) {
	body, err := roleBody(operatorCodePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"Mail may arrive mid-run", "Commander mail system", "quoted `> `", "does not widen your brief", "production or merge authority", "cmdr internal-inbox --agent", "older mail(s)"} {
		if !strings.Contains(body, needle) {
			t.Errorf("operator prompt missing %q", needle)
		}
	}
}

// Mail states it grants no authority; every role must still act on a real answer.
func TestMailAuthorityInPrompts(t *testing.T) {
	for _, file := range []string{"commander.md", "lead.md", "operator-code.md"} {
		body, err := templateFS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, needle := range []string{"an answered card or a closed gate on the bead", "Mail itself grants no authority"} {
			if !strings.Contains(string(body), needle) {
				t.Errorf("%s missing %q", file, needle)
			}
		}
	}
	for _, file := range []string{"commander.md", "lead.md"} {
		body, err := templateFS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, needle := range []string{"A `[decision]` mail reports such an answer", "the bead confirms it"} {
			if !strings.Contains(string(body), needle) {
				t.Errorf("%s missing %q", file, needle)
			}
		}
	}
}

func TestExistingAgentMailRouteInPrompts(t *testing.T) {
	for file, needles := range map[string][]string{
		"commander.md":          {"--to-existing-agent", "Managers, Commander and the user", "your own role", "never grants a contact", "`sent:true`"},
		"lead.md":               {"--to-existing-agent", "Managers, Commander and the user", "your own role", "never grants a contact", "`sent:true`"},
		"operator-code.md":      {"--to-existing-agent", "Managers, Commander and the user", "refused"},
		"operator-knowledge.md": {"--to-existing-agent", "Managers, Commander and the user", "refused"},
	} {
		body, err := templateFS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, needle := range needles {
			if !strings.Contains(string(body), needle) {
				t.Errorf("%s missing %q", file, needle)
			}
		}
	}
}

func TestRenewingStaleWorkflowStepsInLeadPrompt(t *testing.T) {
	body, err := templateFS.ReadFile("lead.md")
	if err != nil {
		t.Fatal(err)
	}
	lead := string(body)
	for _, needle := range []string{
		"A new push requires current-head evidence, not blind repetition",
		"Closing a closed step again reruns its fresh check",
		"cmdr internal-close-objective --project P --bead S",
		"workflow-review-proof@v1 {",
	} {
		if !strings.Contains(lead, needle) {
			t.Errorf("lead.md misses %q", needle)
		}
	}
}

func TestStandingDecisionD9InPrompts(t *testing.T) {
	for file, needles := range map[string][]string{
		"lead.md":          {"every PR needs three review beads before merging", "cmdr workflow plug pr-review", "cmdr workflow bind-writers", "Never mark a PR **reviewed** clean while", "Devin comments are unanswered", "adversarial-only", "one bounded independent challenge", "at most one recovery per failure cause", "never call unavailable execution a source NOT CLEAN"},
		"operator-code.md": {"read-only on the code", "primary and adversarial reviewers are different", "compound-engineering:ce-code-review", "depth:auto", "when policy requires", "not a supported skill", "execution=blocked", "coverage=partial", "another primary roster", "at most one recovery per failure", "The writer answers every Devin comment"},
	} {
		body, err := templateFS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, needle := range needles {
			if !strings.Contains(string(body), needle) {
				t.Errorf("%s missing policy contract %q", file, needle)
			}
		}
		for _, stale := range []string{"at the chosen `depth:focused`", "a hand-only review is NOT CLEAN", "Never read the diff yourself", "until CLEAN ->"} {
			if strings.Contains(string(body), stale) {
				t.Errorf("%s retains contradictory instruction %q", file, stale)
			}
		}
	}
}

func TestBackgroundServiceRuleInLeadAndOperatorPrompts(t *testing.T) {
	for file, needles := range map[string][]string{
		"lead.md":          {"Background-service tasks run on Claude", "--runtime claude", "`machine start`"},
		"operator-code.md": {"## Background services", "--runtime claude", "setsid nohup", "separate follow-up call"},
	} {
		body, err := templateFS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, needle := range needles {
			if !strings.Contains(string(body), needle) {
				t.Errorf("%s missing %q", file, needle)
			}
		}
	}
}

func TestReviewedGateRuleInLeadAndOperatorPrompts(t *testing.T) {
	for file, needles := range map[string][]string{
		"lead.md":          {"**reviewed**", "reviewed by a SEPARATE", "/ce-code-review", "NOT CLEAN"},
		"operator-code.md": {"review Operator", "read-only on the code", "`CLEAN`", "`NOT CLEAN`", "file:line", "depth:focused", "Agent tool", "scope_hash", "`Head: <full 40-character sha"},
	} {
		body, err := templateFS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, needle := range needles {
			if !strings.Contains(string(body), needle) {
				t.Errorf("%s missing %q", file, needle)
			}
		}
	}
}

func TestApprovalsAreGateBeadsInLeadPrompt(t *testing.T) {
	body, err := templateFS.ReadFile("lead.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"Every user approval or greenlight is a `--gate` bead", "--not-a-gate"} {
		if !strings.Contains(string(body), needle) {
			t.Errorf("lead.md missing %q", needle)
		}
	}
}

func TestCreateExitThreeInPrompts(t *testing.T) {
	for file, needles := range map[string][]string{
		"lead.md":          {"Exit 3 from `internal-create-objective`", "`complete:false`", "`unlinked`", "cmdr internal-gate link"},
		"commander.md":     {"Exit 3 is not a failure to create", "`unlinked`"},
		"operator-code.md": {"exit 3 = the bead exists"},
	} {
		body, err := templateFS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, needle := range needles {
			if !strings.Contains(string(body), needle) {
				t.Errorf("%s missing %q", file, needle)
			}
		}
	}
}

func TestBuildSlotPolicyInPrompts(t *testing.T) {
	for file, needles := range map[string][]string{
		"operator-code.md":      {"Build and test slot", "cmdr internal-slot run", "targeted", "go test -p 2", "operator-refusals.jsonl", "pytest path/to/test_x.py::name", "uv run pytest", "just test", "waits behind one\n  holding none", "Never search the whole machine"},
		"operator-knowledge.md": {"Build and test slot", "cmdr internal-slot run", "targeted", "go test -p 2", "pytest path/to/test_x.py::name", "uv run pytest", "Never search the whole machine"},
		"lead.md":               {"Build and test slot", "cmdr internal-slot run", "targeted", "pytest path/to/test_x.py::name", "uv run pytest"},
	} {
		body, err := templateFS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, needle := range needles {
			if !strings.Contains(string(body), needle) {
				t.Errorf("%s missing %q", file, needle)
			}
		}
	}
}

// A refusal is a stop, except that doing what the refusal names is fine; the slot
// rule and the hold rule it must not contradict stay beside it.
func TestStopAtRefusalInOperatorPrompt(t *testing.T) {
	body, err := templateFS.ReadFile("operator-code.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{
		"A command a guard or hook refuses is a stop",
		"report it on your bead and to your Manager",
		"Do not reach the same thing another way",
		"`COMMANDER_WRITE_DOOR`",
		"`COMMANDER_AGENT_WRITE_GUARD`",
		"a script or a scratch test",
		"This binds any agent you start",
		"Following what the refusal itself names is not going around it",
		"A PreToolUse hook refuses the bare forms",
		"`internal-ship` refuses while it is set",
	} {
		if !strings.Contains(string(body), needle) {
			t.Errorf("operator-code.md missing %q", needle)
		}
	}
}

// A separate outcome is a nested workstream; a new top-level one only for a new area.
func TestNestedWorkstreamsInPrompts(t *testing.T) {
	for _, tc := range []struct {
		role    string
		needles []string
	}{
		{RoleCommander, []string{"Workstreams nest: one per outcome", "all workstreams or all steps", "an area of attention the user would rank on its own", "--workstream", "--take-open-steps", "internal-track convert", "internal-move-objective", "cmdr graph check"}},
		{RoleManager, []string{"Workstreams nest: one per outcome", "all workstreams or all steps", "only for a genuinely new area the user would rank on its own", "--parent <workstream> --workstream", "--take-open-steps"}},
	} {
		body, err := roleBody(tc.role)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range tc.needles {
			if !strings.Contains(body, n) {
				t.Errorf("%s prompt lacks %q", tc.role, n)
			}
		}
	}
}

// Operator caps are per workstream, with an override; the per-project cap is gone.
func TestOperatorCapPerWorkstreamInLeadPrompt(t *testing.T) {
	body, err := templateFS.ReadFile("lead.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"capped per workstream", "max_operators_per_workstream", "--max-operators <n>"} {
		if !strings.Contains(string(body), needle) {
			t.Errorf("lead.md missing %q", needle)
		}
	}
	for _, file := range []string{"lead.md", "operator-code.md"} {
		body, _ := templateFS.ReadFile(file)
		if strings.Contains(string(body), "capped per project") || strings.Contains(string(body), "machine-wide\n  slot") {
			t.Errorf("%s still describes a per-project cap or a single machine-wide slot", file)
		}
	}
}

// Prompts preserve defaults and explicit user choices.
func TestPromptsHonorRequestedRuntime(t *testing.T) {
	for _, file := range []string{"commander.md", "lead.md"} {
		body, err := templateFS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, stale := range []string{"Only a person puts an agent on Codex", "standing decision d11"} {
			if strings.Contains(string(body), stale) {
				t.Errorf("%s retains %q", file, stale)
			}
		}
		for _, needle := range []string{"Honor the user's runtime choice", "Claude", "default", "Respect a user pause"} {
			if !strings.Contains(string(body), needle) {
				t.Errorf("%s lacks %q", file, needle)
			}
		}
		if file == "commander.md" && !strings.Contains(string(body), "The Commander chat stays on Claude (standing decision d38)") {
			t.Error("Commander preference missing")
		}
	}
}

// No wait kind names another workstream (beads.ParseWait): the prompts must
// not teach one, and Commander must say what to record across projects.
func TestCrossProjectWaitTeachesOnlyWhatExists(t *testing.T) {
	for _, file := range []string{"commander.md", "lead.md"} {
		body, err := templateFS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, stale := range []string{"recorded wait on the other workstream", "--waiting workstream:"} {
			if strings.Contains(string(body), stale) {
				t.Errorf("%s teaches the missing verb %q", file, stale)
			}
		}
	}
	body, err := templateFS.ReadFile("commander.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"no wait kind names another workstream", "`cmdr internal-comment`", "--waiting until:<RFC3339>:<plain words saying what to recheck>", "`--state needs-user --decision`",
		// An until: wait mails the top-level workstream's Manager (daemon/waits.go),
		// and a future one refuses a later card (externalWait).
		"cmdr internal-set-objective --waiting until:", "mails the Manager of that bead's top-level workstream, not you", "tell that Manager what to read", "refuses a later card", "`--waiting none` first",
		// Typed waits: a full sha is over beads.MaxLabelLen.
		"`ci:<owner/repo>#N` fires when the PR's checks settle, not on merge", "`main-ci:<owner/repo>@<short sha of 7-12 hex>`", "`prod-tag:<owner/repo>@<short sha of 7-12 hex>`",
		// prodTagFor reads one prefix and prodTagsChecked tags.
		"the last 15 tags by name that start with cmdrd's one prefix", "A full sha is too long to record", "a long repo name can be refused",
		// internal-gate link takes any edge; internal-dep refuses kind:track (checkNotGateEdge).
		"`cmdr internal-gate link --project <p> --bead <nested workstream> --after <nested workstream it waits on>`", "`cmdr internal-dep add` refuses a bead still labelled `kind:track`",
		"Work in a different top-level workstream of the same project: `cmdr internal-dep add`, or the same `cmdr internal-gate link` when either end is nested",
		// livedata TestBoardReadsOnlyTheBeadAnEdgePointsAt holds these to the Board.
		"never of what is under it", "An edge to a workstream or a parent step only sets the order", "put the edge on the step doing that work",
	} {
		if !strings.Contains(string(body), needle) {
			t.Errorf("commander.md lacks %q", needle)
		}
	}
	if strings.Contains(string(body), "live Operators through that edge") {
		t.Error("commander.md says the board reads through any edge; a workstream or parent blocker reads Stalled")
	}
	lead, err := templateFS.ReadFile("lead.md")
	if err != nil {
		t.Fatal(err)
	}
	// waitPollInterval is a minute, and the watcher mails only on an event.
	for _, stale := range []string{"polls GitHub every 3 min", "still waiting on X", "nudge"} {
		if strings.Contains(string(lead), stale) {
			t.Errorf("lead.md still says %q", stale)
		}
	}
	for _, needle := range []string{"cmdrd polls GitHub every minute", "there is no reminder", "`Waiting for CI`", "requested changes on|commented on #N", "record a short sha, 7-12 hex"} {
		if !strings.Contains(string(lead), needle) {
			t.Errorf("lead.md lacks %q", needle)
		}
	}
}

func TestUsageLimitSwitchRuleInCommanderAndLeadPrompts(t *testing.T) {
	for file, needles := range map[string][]string{
		"commander.md": {"usage limit", "cmdr account list", "cmdr account use <role> <account>", "internal-operator-provider", "one line", "never log anything in or out"},
		"lead.md":      {"usage limit", "cmdr account list", "--account <name>", "internal-operator-provider <operator-role> --account <name>", "one line"},
	} {
		body, err := templateFS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range needles {
			if !strings.Contains(string(body), n) {
				t.Errorf("%s missing %q", file, n)
			}
		}
	}
}

// A Manager's prompt is refreshed from today's templates but keeps its
// workstream preamble, so a chat start loads the current instructions.
func TestRefreshClaudeAgentKeepsAManagersPreamble(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	pre := "# Your workstream\n\nYou are the Manager of ONE workstream.\n\n- Workstream: X\n- Wherever the instructions below say \"<workstream-id>\" or \"<project>\", use x-1 and p."
	if err := EnsureClaudeAgentWithPreamble(RoleManager, "manager-x-1", pre); err != nil {
		t.Fatal(err)
	}
	path := agentrt.ClaudePromptPath("manager-x-1")
	os.WriteFile(path, []byte(pre+"\n\nan old body"), 0o600)
	if err := RefreshClaudeAgent(RoleManager, "manager-x-1"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(got), pre+"\n\n") || strings.Contains(string(got), "an old body") || !strings.Contains(string(got), "The standard gates") {
		t.Fatalf("refreshed prompt:\n%s", got)
	}
}

func TestProdGateInPrompts(t *testing.T) {
	for file, needles := range map[string][]string{
		"lead.md":          {"--prod-gate", "CLOSED", "prod-gate:<id>"},
		"operator-code.md": {"PRODUCTION GATE", "ONLY the action", "$COMMANDER_PROD_GATE"},
	} {
		body, err := templateFS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, needle := range needles {
			if !strings.Contains(string(body), needle) {
				t.Errorf("%s missing %q", file, needle)
			}
		}
	}
}

func TestApprovesFlagInDecisionCardPrompts(t *testing.T) {
	for _, file := range []string{"lead.md", "commander.md"} {
		body, err := templateFS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"approves": true`) {
			t.Errorf("%s does not tell authors to set \"approves\": true", file)
		}
	}
}

func TestAgentsDoNotTeachDirectBeadsWrites(t *testing.T) {
	// The agent write guard refuses bd/br writes: no prompt may teach one (a
	// prohibition that says so is fine), and the cmdr verbs that replace them are named.
	// Any bd/br followed (past global flags) by a write verb, in backticks or prose.
	write := regexp.MustCompile(`\b(bd|br)\b( (--db|-C|--actor) \S+)* (create|q|update|close|reopen|delete|defer|undefer|set-state|import|edit|rename|move|promote|merge|assign|reparent|sync|dep (add|rm|remove|delete)|label (add|rm|remove|delete)|comments (add|delete|remove|edit|update)|config (set|unset))\b`)
	entries, err := templateFS.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		body, err := templateFS.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for n, line := range strings.Split(string(body), "\n") {
			if write.MatchString(line) && !strings.Contains(line, "Do NOT") && !strings.Contains(line, "do not run bd/br writes") {
				t.Errorf("%s:%d teaches a direct bd/br write: %s", e.Name(), n+1, strings.TrimSpace(line))
			}
		}
	}
	for file, needles := range map[string][]string{
		"lead.md":               {"cmdr internal-dep", "cmdr internal-phase", "--operational", "internal-dep --project <project> rm", "cmdr internal-update", "cmdr internal-label", "--reason", "--deps"},
		"operator-code.md":      {"cmdr internal-comment", "cmdr internal-dep", "--operational"},
		"operator-knowledge.md": {"cmdr internal-phase", "cmdr internal-comment"},
	} {
		body, _ := templateFS.ReadFile(file)
		for _, needle := range needles {
			if !strings.Contains(string(body), needle) {
				t.Errorf("%s missing %q", file, needle)
			}
		}
	}
}

func TestGraphShapeAndLiveGatesInLeadPrompt(t *testing.T) {
	body, err := templateFS.ReadFile("lead.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"Shape the graph", "never rely on the\nid prefix", "`viz:1|2|3`", "cmdr graph check", "**Live gates.**", "latch"} {
		if !strings.Contains(string(body), needle) {
			t.Errorf("lead.md missing %q", needle)
		}
	}
}

// The Manager is taught the stale-card verbs; only cmdr verbs, never a direct beads-CLI write.
func TestPublicationRepairInLeadPrompt(t *testing.T) {
	body, err := templateFS.ReadFile("lead.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"cmdr internal-gate publish", "--repair-ancestor-waits", "dry run first", "other live work"} {
		if !strings.Contains(string(body), needle) {
			t.Errorf("lead.md missing %q", needle)
		}
	}
}

func TestStaleCardVerbsInLeadPrompt(t *testing.T) {
	body, err := templateFS.ReadFile("lead.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"--card-check still-right --reason", "reads **out of date**", "`[stale card]` once per card generation", "--push-reason", "pushed:<n>", "Only Commander and the user reset the count (`--push-reset`)"} {
		if !strings.Contains(string(body), needle) {
			t.Errorf("lead.md missing %q", needle)
		}
	}
}

func TestStandingDecisionsAndProposalsInPrompts(t *testing.T) {
	for file, needles := range map[string][]string{
		"lead.md":      {"standing decisions", "cmdr decisions add", "Run the approved workflow and nothing more", "**proposal**", "Commander is mailed to\naccept or overrule it", "do not add it\nagain"},
		"commander.md": {"You are the commander", "cmdr decisions add", "cmdr proposals list", "cmdr proposals overrule --project <p> --bead <id> --reason", "Never relay a proposal to the user as a\n   question"},
	} {
		body, err := templateFS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, needle := range needles {
			if !strings.Contains(string(body), needle) {
				t.Errorf("%s missing %q", file, needle)
			}
		}
	}
}

func TestRuntimeChoicesDoNotEvadeUserLimits(t *testing.T) {
	for _, file := range []string{"commander.md", "lead.md", "operator-code.md"} {
		body, err := templateFS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, rule := range []string{"Honor the user's explicit runtime choice", "Do not switch runtime to evade a user pause, usage limit, or missing capacity", "Codex requires ready guard hooks"} {
			if !strings.Contains(string(body), rule) {
				t.Errorf("%s lacks %q", file, rule)
			}
		}
	}
}

func TestDeliveryAuthorityAndReadinessContract(t *testing.T) {
	body, err := templateFS.ReadFile("lead.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"Authority and approvals", "do not ask again", "automatic deployment", "deployment runner capability", "restorable rollback inputs", "Progress is evidence", "last real result and time", "current-head delta assessment", "never relabel an old report"} {
		if !strings.Contains(string(body), needle) {
			t.Errorf("missing delivery contract %q", needle)
		}
	}
	if strings.Contains(string(body), "Do the work yourself only when it is") {
		t.Fatal("conflicting Manager edit authority remains")
	}
}

// Every role is taught the one holder rule cmdr enforces, and none teaches a Manager owner as a way to hold work.
func TestHolderRuleInPrompts(t *testing.T) {
	for _, role := range []string{RoleCommander, RoleManager} {
		body, err := roleBody(role)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body, "Every write names who holds the work next") || !strings.Contains(body, "a Manager's name is not a holder") && !strings.Contains(body, "A Manager's name is not a holder") {
			t.Errorf("%s prompt does not teach the holder rule", role)
		}
	}
	op, err := roleBody(operatorCodePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"Final report:", "Never leave a background command running"} {
		if !strings.Contains(op, needle) {
			t.Errorf("operator prompt missing %q", needle)
		}
	}
	for _, role := range []string{RoleCommander, RoleManager, operatorCodePath} {
		body, _ := roleBody(role)
		for _, line := range strings.Split(body, "\n") {
			if strings.Contains(line, "--owner manager-") && !strings.Contains(line, "not a holder") && !strings.Contains(line, "is written with") {
				t.Errorf("%s prompt teaches --owner manager-* as a way to hold work: %s", role, line)
			}
		}
	}
}

// The Operator prompt says why git stash is refused and what to do instead.
func TestOperatorPromptForbidsGitStash(t *testing.T) {
	raw, err := os.ReadFile("operator-code.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Never use `git stash`", "shares one stash list", "WIP commit"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("operator-code.md lacks %q", want)
		}
	}
}

// Nested Managers: the parent dispatches, rolls up and links across siblings; a
// nested Manager reports to its parent; Operators learn a reroute means a new Manager.
func TestNestedManagersInPrompts(t *testing.T) {
	for file, needles := range map[string][]string{
		"lead.md": {
			"NEAREST workstream", "Nested Managers", "--as-parent", "cmdr board --role <your role>",
			"never write beads under a sibling", "cmdr internal-dep --project <project> add <your step> <their step>",
			"opens its own Manager's chat",
		},
		"commander.md":     {"nested Managers on", "cmdr internal-nest-managers --project <p>", "--undo"},
		"operator-code.md": {"rerouted to another", "your Manager changed"},
	} {
		body, err := templateFS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, needle := range needles {
			if !strings.Contains(string(body), needle) {
				t.Errorf("%s missing %q", file, needle)
			}
		}
	}
	if body, _ := templateFS.ReadFile("lead.md"); strings.Contains(string(body), "You stay the one Manager for everything under your top-level workstream, and Operators") {
		t.Error("lead.md still says one Manager owns everything without the nested-Managers condition")
	}
}

// An interrupt reads to Claude as the user rejecting the tool call; Commander
// and Managers must not take that for a hold.
func TestToolRejectionIsNotAHoldInPrompts(t *testing.T) {
	for _, role := range []string{RoleCommander, RoleManager} {
		body, err := roleBody(role)
		if err != nil {
			t.Fatal(err)
		}
		for _, needle := range []string{"[commander interrupt]", "[Request interrupted by user for tool use]", "not a hold", "do what it directs", "user's own Stop", "is a hold", "wait for the user"} {
			if !strings.Contains(body, needle) {
				t.Errorf("%s prompt missing %q", role, needle)
			}
		}
		// The rule is keyed on the note: no wording may tell an agent that an
		// interruption without text is not a hold, since the user's own Stop
		// carries no text either.
		// Only an interrupt can be the user's Stop: Managers meet many tool
		// refusals (guard denies, disallowed tools) that are no hold.
		for _, needle := range []string{"guard or hook refusal", "first message"} {
			if !strings.Contains(body, needle) {
				t.Errorf("%s prompt missing %q", role, needle)
			}
		}
		for _, banned := range []string{"with no user text is not a hold", "no user text is not a hold", "A tool rejection is the user saying stop"} {
			if strings.Contains(body, banned) {
				t.Errorf("%s prompt says %q: that overrides the user's own Stop", role, banned)
			}
		}
	}
}
