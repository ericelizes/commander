package spawn

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/store"
)

// NativeExecutionRuntime uses the daemon's same run-bound chat engine. The
// detached host owns initial instruction delivery and observed exit recovery.
type NativeExecutionRuntime interface {
	Start(context.Context, string, chat.StartOptions) (chat.Status, error)
}

func (s *Spawner) WithNativeExecution(runtime NativeExecutionRuntime) *Spawner {
	s.nativeExecution = runtime
	return s
}

// A non-nil result means custody was committed. Preserve the workspace/run on
// a transport error: recovery must retry the same identity, not launch a second
// process or remove a workspace that an unobserved host may still be using.
func (s *Spawner) spawnNativeClaim(ctx context.Context, req SpawnRequest, agent, workspace, sessionFile string) (*SpawnResult, error) {
	if s.nativeExecution == nil {
		return nil, errors.New("claimed execution requires the run-bound native runtime")
	}
	sid := agentrt.NewClaudeSessionID()
	if req.Resume {
		sid = checkpointSessionID(req, sessionFile)
		if sid == "" {
			return nil, errors.New("native execution checkpoint has no retained session")
		}
	}
	if e := validateCheckpointSession(req, agent, sessionFile, sid); e != nil {
		return nil, e
	}
	if e := prepareNativeCheckpoint(req, agent); e != nil {
		return nil, e
	}
	at := s.now()
	rid, e := s.store.InsertAgentRun(ctx, store.AgentRun{ExecutionClaimToken: req.ExecutionClaimToken, ResumeSourceRunID: req.ResumeSourceRunID, ExecutionDriver: "native", ExecutionRuntime: req.Runtime, ExecutionAccount: req.Account, ExecutionModel: req.Model, ProjectID: req.ProjectID, TaskBeadID: req.TaskBeadID, AgentName: agent, OpencodeSessionID: sid, SpawnTime: at.Unix(), WorktreePath: sql.NullString{String: workspace, Valid: workspace != ""}})
	if e != nil {
		return nil, e
	}
	result := &SpawnResult{RunID: rid, SessionID: sid, AgentName: agent, WorktreePath: workspace, SpawnTime: at, Runtime: req.Runtime, Account: req.Account, Moved: req.moved}
	if e = writeSessionFile(sessionFile, sid); e != nil {
		return result, fmt.Errorf("native execution %d retained; persist session: %w", rid, e)
	}
	s.recordRuntime(ctx, req, agent, at.Unix())
	status, e := s.nativeExecution.Start(ctx, agent, chat.StartOptions{})
	if e == nil && (status.SessionID != sid || status.Runtime != req.Runtime) {
		e = errors.New("native runtime returned a different session/provider")
	}
	if recorder, ok := s.store.(interface {
		RecordNativeStartupAttempt(context.Context, string, int64, string, string, int64) error
	}); ok {
		detail := ""
		if e != nil {
			detail = e.Error()
		}
		if recordErr := recorder.RecordNativeStartupAttempt(ctx, agent, rid, sid, detail, s.now().Unix()); recordErr != nil {
			e = errors.Join(e, recordErr)
		}
	}
	if e != nil {
		return result, fmt.Errorf("native execution %d retained for recovery: %w", rid, e)
	}
	return result, nil
}
