package promptassets

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/example-user/commander/internal/agentrt"
)

const (
	CommanderAgentName = "commander"

	commanderTemplatePath = "commander.md"
	leadTemplatePath      = "lead.md"
	operatorCodePath      = "operator-code.md"
	operatorKnowledgePath = "operator-knowledge.md"
	ownershipPath         = "ownership.md" // shared by Commander and Managers: what "own this" means
)

//go:embed *.md
var templateFS embed.FS

func EnsureCommanderAgent() error {
	return ensureTemplateFile(CommanderAgentName, commanderTemplatePath)
}

func EnsureManagerAgent(agentName string) error {
	return ensureTemplateFile(agentName, leadTemplatePath)
}

func EnsureLeadAgent(agentName string) error {
	return EnsureManagerAgent(agentName)
}

func EnsureOperatorCodeAgent(agentName string) error {
	return ensureTemplateFile(agentName, operatorCodePath)
}

func EnsureOperatorKnowledgeAgent(agentName string) error {
	return ensureTemplateFile(agentName, operatorKnowledgePath)
}

// Claude agent roles, keyed to the same templates the opencode agents use.
const (
	RoleCommander         = commanderTemplatePath
	RoleManager           = leadTemplatePath
	RoleOperatorCode      = operatorCodePath
	RoleOperatorKnowledge = operatorKnowledgePath
)

// EnsureAgentFor installs agentName for the given runtime: the opencode agent
// file, or the Claude Code agent file translated from the same template.
func EnsureAgentFor(kind agentrt.Kind, role, agentName string) error {
	if kind == agentrt.Claude {
		return EnsureClaudeAgent(role, agentName)
	}
	return ensureTemplateFile(agentName, role)
}

// EnsureClaudeAgent writes the role prompt for agentName to
// agentrt.ClaudePromptPath, which Claude panes load with
// --append-system-prompt-file. Only the body is kept: the opencode
// frontmatter (description, glob tool map) has no meaning there, and the
// agent-mail deny is passed as --disallowedTools instead.
func EnsureClaudeAgent(role, agentName string) error {
	body, err := roleBody(role)
	if err != nil {
		return err
	}
	out := body
	if _, err := agentrt.ClaudePromptDir(); err != nil {
		return err
	}
	target := agentrt.ClaudePromptPath(agentName)
	if current, err := os.ReadFile(target); err == nil && string(current) == out {
		return nil
	}
	return writeFileAtomically(target, []byte(out))
}

// RefreshClaudeAgent rewrites agentName's prompt file from the current
// templates, keeping a Manager's workstream preamble (the "# Your workstream"
// block the file starts with), so a chat that starts loads today's prompt.
func RefreshClaudeAgent(role, agentName string) error {
	if role == RoleManager {
		if cur, err := os.ReadFile(agentrt.ClaudePromptPath(agentName)); err == nil {
			if pre := managerPreamble(string(cur)); pre != "" {
				return EnsureClaudeAgentWithPreamble(role, agentName, pre)
			}
		}
	}
	return EnsureClaudeAgent(role, agentName)
}

// managerPreamble is the "# Your workstream" block a Manager's prompt file
// starts with (wsmgr.Preamble), through its last line; "" when it has none.
func managerPreamble(file string) string {
	if !strings.HasPrefix(file, "# Your workstream") {
		return ""
	}
	i := strings.Index(file, "\n- Wherever the instructions below say")
	if i < 0 {
		return ""
	}
	end := strings.Index(file[i+1:], "\n")
	if end < 0 {
		return file
	}
	return file[:i+1+end]
}

// EnsureClaudeAgentWithPreamble is EnsureClaudeAgent with preamble prepended to
// the role body, e.g. the workstream a Manager owns.
func EnsureClaudeAgentWithPreamble(role, agentName, preamble string) error {
	body, err := roleBody(role)
	if err != nil {
		return err
	}
	out := strings.TrimRight(preamble, "\n") + "\n\n" + body
	if _, err := agentrt.ClaudePromptDir(); err != nil {
		return err
	}
	target := agentrt.ClaudePromptPath(agentName)
	if current, err := os.ReadFile(target); err == nil && string(current) == out {
		return nil
	}
	return writeFileAtomically(target, []byte(out))
}

// roleBody is a role's prompt: its template without frontmatter, plus the
// ownership contracts for the roles that hand off or own work (Commander and
// Managers), so both read the same definition of "own this".
func roleBody(role string) (string, error) {
	tmpl, err := templateFS.ReadFile(role)
	if err != nil {
		return "", fmt.Errorf("read embedded prompt template %s: %w", role, err)
	}
	out := frontmatterBody(string(tmpl))
	if role == RoleCommander || role == RoleManager {
		contracts, err := templateFS.ReadFile(ownershipPath)
		if err != nil {
			return "", fmt.Errorf("read embedded prompt template %s: %w", ownershipPath, err)
		}
		out = strings.TrimRight(out, "\n") + "\n\n" + string(contracts)
	}
	return out, nil
}

// frontmatterBody returns tmpl without its leading YAML frontmatter.
func frontmatterBody(tmpl string) string {
	rest, ok := strings.CutPrefix(tmpl, "---\n")
	if !ok {
		return tmpl
	}
	if _, body, ok := strings.Cut(rest, "\n---\n"); ok {
		return strings.TrimLeft(body, "\n")
	}
	return tmpl
}

func CommanderWorkspaceRoot() (string, error) {
	if root := os.Getenv("COMMANDER_WORKSPACE_ROOT"); root != "" {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return "", fmt.Errorf("mkdir workspace root %s: %w", root, err)
		}
		return root, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	root := filepath.Join(home, "code")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("mkdir workspace root %s: %w", root, err)
	}
	return root, nil
}

func AgentDir() (string, error) {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		configHome = filepath.Join(home, ".config")
	}
	dir := filepath.Join(configHome, "opencode", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir agent dir %s: %w", dir, err)
	}
	return dir, nil
}

func ensureTemplateFile(agentName, templatePath string) error {
	body, err := templateFS.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("read embedded prompt template %s: %w", templatePath, err)
	}
	dir, err := AgentDir()
	if err != nil {
		return err
	}
	target := filepath.Join(dir, agentName+".md")
	current, err := os.ReadFile(target)
	if err == nil && string(current) == string(body) {
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read existing agent prompt %s: %w", target, err)
	}
	return writeFileAtomically(target, body)
}

func writeFileAtomically(path string, body []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+strings.TrimSuffix(filepath.Base(path), ".md")+".*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
