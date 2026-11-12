package execution

import (
	"context"
	"errors"
	"fmt"
	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/chat"
	"os/exec"
	"strings"

	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
)

func (w *Worker) resumeQuestionCheckpoint(ctx context.Context, c store.ExecutionClaim, r *spawn.SpawnRequest) error {
	if c.ResumeRunID == 0 {
		return nil
	}
	prior, e := w.Store.GetAgentRun(ctx, c.ResumeRunID)
	if e != nil {
		return e
	}
	if prior == nil || prior.ProjectID != c.Project.ID || prior.TaskBeadID != c.BeadID || prior.Status == "running" {
		return errors.New("question checkpoint is not a terminal execution of this bead")
	}
	r.Name = strings.TrimPrefix(prior.AgentName, "operator-")
	r.Resume = true
	r.ResumeSourceRunID = prior.ID
	usageRecovery, err := w.Store.UsageRecoverySource(ctx, prior.ID)
	if err != nil {
		return err
	}
	if usageRecovery && prior.ExecutionDriver == "native" {
		r.UsageRecovery = true
		r.Runtime = chat.ReadRuntimePin(agentrt.CommanderHome(), prior.AgentName)
		return resumeUsageWorkspace(ctx, c, r, *prior)
	}
	r.ResumeSessionID = prior.OpencodeSessionID
	if r.ResumeSessionID == "" {
		return errors.New("question checkpoint has no recorded runtime session")
	}
	r.ResumeRuntime, r.ResumeNativeThread, e = w.Store.QuestionNativeCheckpoint(ctx, prior.ID)
	if e != nil {
		return e
	}
	if r.ResumeRuntime == "" {
		return errors.New("question checkpoint has no recorded provider")
	}
	// Runtime carries explicit intent; ResumeRuntime binds only this continuation.
	r.Runtime = chat.ReadRuntimePin(agentrt.CommanderHome(), prior.AgentName)
	if prior.ExecutionDriver == "native" {
		if prior.ExecutionRuntime != r.ResumeRuntime || prior.ExecutionAccount == "" || prior.ExecutionModel == "" {
			return errors.New("native checkpoint disagrees with its recorded provider binding")
		}
		r.ResumeDriver = prior.ExecutionDriver
		r.ResumeAccount = prior.ExecutionAccount
		r.Account = prior.ExecutionAccount
		r.Model = prior.ExecutionModel
	}
	r.ResumeNote = fmt.Sprintf("A recorded planning decision is ready. Continue the same checkpoint and session. Read `cmdr question history --project-id %d --bead %s --run-id %d` and the current bead before continuing; answers do not replace release approval gates.", c.Project.ID, c.BeadID, c.ResumeRunID)
	if c.Project.Type == "code" {
		path := spawn.RunWorktree(c.Project.Path, *prior)
		raw, e := exec.CommandContext(ctx, "git", "-C", path, "symbolic-ref", "--quiet", "--short", "HEAD").Output()
		if e != nil {
			return fmt.Errorf("read checkpoint branch: %w", e)
		}
		r.Branch = strings.TrimSpace(string(raw))
		if r.Branch == "" {
			return errors.New("question checkpoint has no resumable branch")
		}
	}
	return nil
}

func resumeUsageWorkspace(ctx context.Context, c store.ExecutionClaim, r *spawn.SpawnRequest, prior store.AgentRun) error {
	if c.Project.Type != "code" {
		return nil
	}
	path := spawn.RunWorktree(c.Project.Path, prior)
	raw, e := exec.CommandContext(ctx, "git", "-C", path, "symbolic-ref", "--quiet", "--short", "HEAD").Output()
	if e != nil {
		return fmt.Errorf("read usage recovery branch: %w", e)
	}
	r.Branch = strings.TrimSpace(string(raw))
	if r.Branch == "" {
		return errors.New("usage recovery has no retained branch")
	}
	return nil
}
