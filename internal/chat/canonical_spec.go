package chat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/example-user/commander/internal/agentrt"
	promptassets "github.com/example-user/commander/prompts"
)

// Active native chats resolve canonical custody before role-name conventions.
func (r StoreResolver) resolveCanonical(ctx context.Context, role string) (Spec, bool, error) {
	if r.Store == nil || !r.Store.IsPostgres() {
		return Spec{}, false, nil
	}
	active, e := r.Store.ActiveOrchestration(ctx)
	if e != nil {
		return Spec{}, true, e
	}
	execution, e := r.Store.NativeExecution(ctx, role)
	if e != nil {
		return Spec{}, true, e
	}
	if !active && (execution == nil || execution.Driver != "native") {
		return Spec{}, false, nil
	}
	if execution != nil && !active {
		execution.CanRestart = false
	}
	planner, e := r.Store.GetCommander(ctx, role)
	if e != nil {
		return Spec{}, true, e
	}
	owns, retired, e := r.Store.PlanningIdentityState(ctx, role)
	if e != nil {
		return Spec{}, true, e
	}
	sessionPath := filepath.Join(r.Home, agentrt.SessionFileName(role, agentrt.Claude))
	if planner != nil && !IsCommanderRole(role) {
		row, e := r.Store.GetWorkstreamManagerByRole(ctx, role)
		if e != nil {
			return Spec{}, true, e
		}
		if row != nil && row.SessionFile != "" {
			sessionPath = row.SessionFile
		}
	}
	logicalSession, e := readSessionFile(sessionPath)
	if e != nil {
		return Spec{}, true, e
	}
	spec := Spec{Role: role, Agent: role, AllowedTools: agentrt.ClaudeAllowedTools, SkipPermissions: os.Getenv("COMMANDER_CHAT_PERMISSIONS") != "prompt"}
	if execution != nil {
		if planner != nil && owns && logicalSession != "" && logicalSession != execution.SessionID {
			return Spec{}, true, errors.New("native role has distinct planning and execution sessions; use separate runtime identities")
		}
		spec.ExecutionRunID = execution.RunID
		spec.ExecutionBlocked = !execution.CanRestart
		spec.SessionID = execution.SessionID
		spec.Tier = agentrt.TierOperator
		spec.Cwd = execution.ProjectPath
		if execution.ProjectType == "code" {
			if execution.Worktree == "" {
				return Spec{}, true, errors.New("native code execution has no recorded worktree")
			}
			spec.Cwd = execution.Worktree
		}
		if !filepath.IsAbs(spec.Cwd) {
			return Spec{}, true, errors.New("native execution requires its recorded absolute workspace")
		}
		quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
		preamble := fmt.Sprintf("# Your assigned canonical bead\nProject: %s\nBead: %s\nTitle: %s\nBrief:\n%s\nAcceptance:\n%s\nCurrent planning Commander: %s\nRead current state: cmdr bead --project-dir %s show %s\nExecution identity comes from the immutable run/session environment. Do not replace it with planning authority.\n", execution.ProjectPath, execution.BeadID, execution.Title, execution.Description, execution.Acceptance, execution.PlanningCommander, quote(execution.ProjectPath), quote(execution.BeadID))
		if e = promptassets.EnsureClaudeAgentWithPreamble(promptassets.RoleExecutor, spec.Agent, preamble); e != nil {
			return Spec{}, true, e
		}
		model := execution.Model
		if model == "" {
			model = agentrt.Model(spec.Tier)
		}
		spec.Model = agentrt.ClaudeModel(model)
		spec = WithAccount(WithRuntime(spec, r.Home), r.Home)
		spec.Handoff = ""
		if execution.Driver == "native" {
			if execution.Runtime != RuntimeClaude && execution.Runtime != RuntimeCodex {
				return Spec{}, true, errors.New("native run has no recorded provider")
			}
			spec.Runtime = execution.Runtime
			spec.Account = execution.Account
			spec.Model = agentrt.ClaudeModel(model)
			spec.ExecutionResume = execution.ResumeSourceRunID > 0 && !execution.UsageRecovery
			spec.ExecutionInitialPrompt = fmt.Sprintf("Execute your assigned canonical bead %s. Read the current bead, goals, constraints and recorded decisions before acting. Work within the supplied acceptance criteria; record evidence and a final report. Ask the current planning Commander only for choices outside your authority. A process exit is not bead completion.", execution.BeadID)
			if execution.UsageRecovery {
				handoff, err := r.usageRecoveryHandoff(ctx, execution.ResumeSourceRunID, execution.ProjectID, execution.BeadID)
				if err != nil {
					return Spec{}, true, err
				}
				spec.ExecutionInitialPrompt += handoff
				spec.ExecutionInitialPrompt += fmt.Sprintf("\nRead any saved answers using cmdr question history --project-id %d --bead %s --run-id %d. Saved answers do not waive release gates.", execution.ProjectID, quote(execution.BeadID), execution.ResumeSourceRunID)
			} else if execution.ResumeSourceRunID > 0 {
				spec.ExecutionInitialPrompt += fmt.Sprintf(" Continue the saved checkpoint from run %d and read its answer using cmdr question history --project-id %d --bead %s --run-id %d. Saved answers do not waive release gates.", execution.ResumeSourceRunID, execution.ProjectID, quote(execution.BeadID), execution.ResumeSourceRunID)
			}
		}
		if spec.Runtime == RuntimeCodex {
			spec.Model = CodexModel(model)
		}
	} else {
		if planner == nil {
			return Spec{}, true, fmt.Errorf("native execution %s has no current canonical claim", role)
		}
		if retired && !owns {
			return Spec{}, true, errors.New("this imported planning runtime is retired; its conversation history is retained")
		}
		if logicalSession == "" {
			if !IsCommanderRole(role) {
				return Spec{}, true, errors.New("registered planning conversation has no retained session")
			}
			if e = r.NewSession(ctx, role); e != nil {
				return Spec{}, true, e
			}
			logicalSession, e = readSessionFile(sessionPath)
			if e != nil {
				return Spec{}, true, e
			}
		}
		spec.SessionID = logicalSession
		spec.Tier = agentrt.TierCommander
		spec.Cwd, e = promptassets.CommanderWorkspaceRoot()
		if e != nil {
			return Spec{}, true, e
		}
		if e = promptassets.RefreshClaudeAgent(promptassets.RolePlanningCommander, spec.Agent); e != nil {
			return Spec{}, true, e
		}
		spec.Model = agentrt.ClaudeModel(agentrt.Model(spec.Tier))
		spec = WithAccount(WithRuntime(spec, r.Home), r.Home)
	}
	spec.Settings = agentrt.ClaudeMailHookSettings(spec.Agent)
	spec.PromptPath = agentrt.ClaudePromptPath(spec.Agent)
	return spec, true, nil
}
