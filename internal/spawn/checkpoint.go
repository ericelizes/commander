package spawn

import (
	"errors"
	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/chat"
	"os"
	"path/filepath"
	"strings"
)

// A recorded question checkpoint is stricter than the legacy best-effort
// resume path. Missing or replaced native state requires explicit recovery;
// never silently mint a fresh conversation and call it the same session.
func validateCheckpointSession(req SpawnRequest, agent, sessionFile, sessionID string) error {
	if req.ResumeSourceRunID == 0 {
		return nil
	}
	if !req.Resume || req.ResumeSessionID == "" || sessionID != req.ResumeSessionID || (req.ResumeDriver != "native" && readSessionFile(sessionFile) != req.ResumeSessionID) {
		return errors.New("recorded checkpoint session metadata is missing or changed")
	}
	if req.ResumeRuntime == "" || req.ResumeRuntime != req.Runtime {
		return errors.New("checkpoint provider differs; a validated conversation handoff is required")
	}
	if req.ResumeDriver == "native" && (req.ResumeAccount == "" || req.Account != req.ResumeAccount) {
		return errors.New("checkpoint account differs; a validated conversation handoff is required")
	}
	if req.Runtime == chat.RuntimeCodex {
		path := chat.CodexThreadFileForRun(agentrt.CommanderHome(), agent, req.ResumeSourceRunID, req.ResumeSessionID)
		if req.ResumeDriver == "native" {
			path = chat.NativeCodexThreadFile(agentrt.CommanderHome(), agent, req.ResumeSessionID)
		}
		if req.ResumeNativeThread == "" {
			return errors.New("checkpoint has no saved native conversation")
		}
		raw, err := os.ReadFile(path)
		// The file is a derived cursor, not the conversation. A native source can
		// restore its exact DB-saved cursor; the bridge must then require the server
		// to resume that thread. Existing different cursors are never overwritten.
		if err != nil {
			if !(req.ResumeDriver == "native" && os.IsNotExist(err)) {
				return errors.New("recorded checkpoint native thread is unavailable")
			}
		} else if strings.TrimSpace(string(raw)) != req.ResumeNativeThread {
			return errors.New("recorded checkpoint native thread changed")
		}
	} else if !claudeCanResume(req, sessionID) {
		return errors.New("recorded checkpoint transcript is unavailable; restore it or authorize a validated handoff")
	}
	return nil
}

func checkpointSessionID(req SpawnRequest, sessionFile string) string {
	if req.ResumeDriver == "native" && req.ResumeSourceRunID > 0 {
		return req.ResumeSessionID
	}
	return readSessionFile(sessionFile)
}

// A legacy Codex checkpoint can enter the new native host only with its exact
// saved thread. Native source paths do not depend on derived chat metadata.
// Never overwrite another conversation's thread while promoting this context.
func prepareNativeCheckpoint(req SpawnRequest, agent string) error {
	if req.ResumeSourceRunID == 0 || req.Runtime != chat.RuntimeCodex {
		return nil
	}
	target := chat.NativeCodexThreadFile(agentrt.CommanderHome(), agent, req.ResumeSessionID)
	if raw, err := os.ReadFile(target); err == nil {
		if strings.TrimSpace(string(raw)) != req.ResumeNativeThread {
			return errors.New("native checkpoint thread conflicts with its saved conversation")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if req.ResumeNativeThread == "" {
		return errors.New("checkpoint has no saved native thread")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		raw, readErr := os.ReadFile(target)
		if readErr == nil && strings.TrimSpace(string(raw)) == req.ResumeNativeThread {
			return nil
		}
		return errors.New("checkpoint thread was concurrently replaced")
	}
	if err != nil {
		return err
	}
	_, err = file.WriteString(req.ResumeNativeThread + "\n")
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(target)
	}
	return err
}
