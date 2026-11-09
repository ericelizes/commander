package chat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/store"
	promptassets "github.com/example-user/commander/prompts"
)

// errNoSession: the Manager has no registration or no session file yet, so no process
// can hold a session of its.
var errNoSession = errors.New("no session")

// Spec is everything needed to run one role's Claude session.
type Spec struct {
	Role       string
	Agent      string // -n name and prompt file base (usually the role)
	SessionID  string
	Cwd        string
	Model      string // Claude --model value; "" = Claude's default
	PromptPath string // --append-system-prompt-file; "" = none
	// Settings is the inline --settings JSON (the mail hooks); "" = none.
	Settings string
	// SkipPermissions runs with --dangerously-skip-permissions (as the tmux
	// cockpit does). Off by default: tool calls outside AllowedTools raise
	// permission_request events the UI answers.
	SkipPermissions bool
	AllowedTools    string // --allowedTools; "" = none
	Tier            agentrt.Tier
	// Runtime is the agent CLI: RuntimeClaude (default) or RuntimeCodex.
	Runtime string
	// Account is the Claude account (claudeacct) the role runs on; "" = default.
	Account string
	// Handoff is a predecessor transcript the role's first message reads first
	// ("" = none): a fresh Codex thread, or Claude back from Codex.
	Handoff string
}

// Agent CLIs a chat can run on.
const (
	RuntimeClaude = "claude"
	RuntimeCodex  = "codex"
)

// RuntimeFile holds a role's runtime choice ("claude" or "codex"); missing = claude.
func RuntimeFile(home, role string) string { return filepath.Join(home, "chat", role+".runtime") }

// ReadRuntime is role's runtime: RuntimeCodex when its runtime file says so, else RuntimeClaude.
func ReadRuntime(home, role string) string {
	raw, err := os.ReadFile(RuntimeFile(home, role))
	if err == nil && strings.TrimSpace(string(raw)) == RuntimeCodex {
		return RuntimeCodex
	}
	return RuntimeClaude
}

// WithRuntime sets spec's runtime from the role's runtime file, and the
// handoff its first message reads: the conversation log, for a role's first
// Codex thread or for Claude right after a switch back from Codex.
func WithRuntime(spec Spec, home string) Spec {
	log := filepath.Join(home, "chat", spec.Role+".jsonl")
	if spec.Runtime = ReadRuntime(home, spec.Role); spec.Runtime == RuntimeCodex {
		spec.Model = CodexModel("") // Commander's Codex model; Claude model names mean nothing to it
		if !fileExists(CodexThreadFile(home, spec.Role)) && fileExists(log) {
			spec.Handoff = log
		}
	} else if fileExists(HandoffFile(home, spec.Role)) {
		spec.Handoff = log
	}
	return spec
}

// Resolver maps a role to its Spec.
type Resolver interface {
	Resolve(ctx context.Context, role string) (Spec, error)
}

// ResolverFunc adapts a function to Resolver.
type ResolverFunc func(ctx context.Context, role string) (Spec, error)

// Resolve calls f.
func (f ResolverFunc) Resolve(ctx context.Context, role string) (Spec, error) { return f(ctx, role) }

var roleRe = regexp.MustCompile(`^(commander|(manager|operator)-[A-Za-z0-9][A-Za-z0-9._-]{0,120})$`)

// ValidRole reports whether role is a chat role: commander, manager-<bead> or
// operator-<flat-branch>. It also keeps roles safe as file names.
func ValidRole(role string) bool { return roleRe.MatchString(role) && !strings.Contains(role, "..") }

// RequiredCapabilities are init.capabilities the engine relies on; a missing
// one is reported as a notice (the protocol may have drifted).
var RequiredCapabilities = []string{"interrupt_receipt_v1"}

// PinnedCLIVersion is the Claude Code version the protocol was verified
// against. A different version still runs, with a notice.
const PinnedCLIVersion = "2.1.305"

// Args is the claude argv (without the binary) for spec. resume selects
// --resume (the session has a transcript) over --session-id.
func Args(spec Spec, resume bool) []string {
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--include-partial-messages", "--include-hook-events", "--permission-prompt-tool", "stdio"}
	if spec.Agent != "" {
		args = append(args, "-n", spec.Agent)
	}
	if spec.PromptPath != "" {
		args = append(args, "--append-system-prompt-file", spec.PromptPath)
	}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	// --disallowedTools/--allowedTools are variadic: always follow them with
	// another flag, never a bare value.
	args = append(args, "--disallowedTools", agentrt.ClaudeDisallowedToolsFor(spec.Agent))
	if spec.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	} else if spec.AllowedTools != "" {
		args = append(args, "--allowedTools", spec.AllowedTools)
	}
	if spec.Settings != "" {
		args = append(args, "--settings", spec.Settings)
	}
	if resume {
		args = append(args, "--resume", spec.SessionID)
	} else {
		args = append(args, "--session-id", spec.SessionID)
	}
	return args
}

// StoreResolver resolves the real cockpit roles:
//
//   - commander: $COMMANDER_HOME/commander.claude.session-id, cwd the
//     workspace root (~/code), the commander prompt.
//   - manager-<bead>: the workstream_managers row (session file, project dir).
//   - operator-<flat>: <project>/.commander/operators/<flat>.claude.session-id,
//     cwd its worktree <project>/.worktrees/<flat>.
//
// It never mints a session for a Manager or Operator: those are created by
// the existing spawn/workstream code.
type StoreResolver struct {
	Store *store.Store
	Home  string // COMMANDER_HOME
}

// Resolve implements Resolver.
func (r StoreResolver) Resolve(ctx context.Context, role string) (Spec, error) {
	if !ValidRole(role) {
		return Spec{}, fmt.Errorf("chat: invalid role %q", role)
	}
	// the user's setting (same as the tmux cockpit's --dangerously-skip-permissions): no
	// approval prompts. COMMANDER_CHAT_PERMISSIONS=prompt makes native chats ask
	// again (the UI answers permission_request events).
	spec := Spec{Role: role, Agent: role, AllowedTools: agentrt.ClaudeAllowedTools,
		SkipPermissions: os.Getenv("COMMANDER_CHAT_PERMISSIONS") != "prompt"}
	switch {
	case role == "commander":
		spec.Agent = promptassets.CommanderAgentName
		spec.Tier = agentrt.TierCommander
		id, err := readSessionFile(filepath.Join(r.Home, agentrt.SessionFileName("commander", agentrt.Claude)))
		if err != nil {
			return Spec{}, err
		}
		if id == "" {
			return Spec{}, fmt.Errorf("chat: no Claude commander session yet (start the cockpit once)")
		}
		spec.SessionID = id
		root, err := promptassets.CommanderWorkspaceRoot()
		if err != nil {
			return Spec{}, err
		}
		spec.Cwd = root
	case strings.HasPrefix(role, "manager-"):
		spec.Tier = agentrt.TierLead
		if r.Store == nil {
			return Spec{}, fmt.Errorf("chat: no store to resolve %s", role)
		}
		row, err := r.Store.GetWorkstreamManagerByRole(ctx, role)
		if err != nil {
			return Spec{}, err
		}
		if row == nil {
			return Spec{}, fmt.Errorf("chat: no workstream Manager %s: %w", role, errNoSession)
		}
		proj, err := r.Store.GetProjectByID(ctx, row.ProjectID)
		if err != nil || proj == nil {
			return Spec{}, fmt.Errorf("chat: project of %s: %v", role, err)
		}
		id, err := readSessionFile(row.SessionFile)
		if err != nil {
			return Spec{}, fmt.Errorf("chat: session of %s: %w", role, err)
		}
		if id == "" {
			return Spec{}, fmt.Errorf("chat: session of %s: %w", role, errNoSession)
		}
		spec.SessionID, spec.Cwd = id, proj.Path
	default: // operator-<flat>
		spec.Tier = agentrt.TierOperator
		if r.Store == nil {
			return Spec{}, fmt.Errorf("chat: no store to resolve %s", role)
		}
		flat := strings.TrimPrefix(role, "operator-")
		projects, err := r.Store.ListProjects(ctx)
		if err != nil {
			return Spec{}, err
		}
		for _, p := range projects {
			id, _ := readSessionFile(filepath.Join(p.Path, ".commander", "operators", agentrt.SessionFileName(flat, agentrt.Claude)))
			if id == "" {
				continue
			}
			spec.SessionID, spec.Cwd = id, filepath.Join(p.Path, ".worktrees", flat)
			break
		}
		if spec.SessionID == "" {
			return Spec{}, fmt.Errorf("chat: no Claude operator session for %s", role)
		}
	}
	// A chat loads its prompt once, at start: write today's first.
	switch {
	case role == "commander":
		if err := promptassets.RefreshClaudeAgent(promptassets.RoleCommander, spec.Agent); err != nil {
			return Spec{}, err
		}
	case strings.HasPrefix(role, "manager-"):
		if err := promptassets.RefreshClaudeAgent(promptassets.RoleManager, spec.Agent); err != nil {
			return Spec{}, err
		}
	}
	spec.Model = agentrt.ClaudeModel(agentrt.Model(spec.Tier))
	spec = WithAccount(WithRuntime(spec, r.Home), r.Home)
	// Same hook role as the tmux chat (agentrt.ClaudeInteractiveCmd uses the agent name).
	spec.Settings = agentrt.ClaudeMailHookSettings(spec.Agent)
	if p := agentrt.ClaudePromptPath(spec.Agent); fileExists(p) {
		spec.PromptPath = p
	}
	return spec, nil
}

// NewSession gives role a brand-new session id (its next start is a fresh
// session, not a resume). Commander and Managers only.
func (r StoreResolver) NewSession(ctx context.Context, role string) error {
	path := ""
	switch {
	case role == "commander":
		path = filepath.Join(r.Home, agentrt.SessionFileName("commander", agentrt.Claude))
	case strings.HasPrefix(role, "manager-") && r.Store != nil:
		row, err := r.Store.GetWorkstreamManagerByRole(ctx, role)
		if err != nil {
			return err
		}
		if row == nil || row.SessionFile == "" {
			return fmt.Errorf("chat: no workstream Manager %s", role)
		}
		path = row.SessionFile
	default:
		return fmt.Errorf("chat: %s cannot start fresh (Commander and Managers only)", role)
	}
	return os.WriteFile(path, []byte(agentrt.NewClaudeSessionID()+"\n"), 0o600)
}

func readSessionFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
