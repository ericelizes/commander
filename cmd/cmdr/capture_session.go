package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/layout"
	promptassets "github.com/example-user/commander/prompts"
)

// ErrOpencodeUnavailable tags failures where opencode itself could not mint the
// Commander session (not installed, or its config is invalid). Commander renders
// its chat panes through opencode, so this is unrecoverable — but the top-level
// CLI uses errors.Is to turn it into an actionable hint instead of a raw dump.
var ErrOpencodeUnavailable = errors.New("opencode could not create the Commander session")

// init registers the production CaptureFn the layout package uses to mint a
// fresh Commander tier opencode session id when ~/.commander/commander.session-id
// is missing. The capture path runs `opencode run --format json` ONCE with a
// terse seed prompt, drains the event stream, and returns the captured id.
//
// We register at init time so the layout package doesn't need to import
// internal/opencode (which would pull a thicker dependency graph into a
// package whose only job is driving tmux).
func init() {
	layout.SetCommanderCaptureFn(captureCommanderSession)
}

// captureCommanderSession creates a blank Commander tier opencode session by
// importing a minimal session JSON. This avoids leaving a visible bootstrap
// prompt in the transcript just to mint a session id.
func captureCommanderSession(ctx context.Context) (string, error) {
	template, err := layout.CommanderPromptTemplate(ctx, agentrt.CommanderHome())
	if err != nil {
		return "", err
	}
	if err := promptassets.EnsureAgentFor(agentrt.Opencode, template, promptassets.CommanderAgentName); err != nil {
		return "", fmt.Errorf("ensure commander agent prompt: %w", err)
	}
	workspaceRoot, err := promptassets.CommanderWorkspaceRoot()
	if err != nil {
		return "", err
	}

	sessionID := newCommanderSessionID()
	payload, err := marshalCommanderBlankSession(sessionID, workspaceRoot, time.Now())
	if err != nil {
		return "", fmt.Errorf("marshal commander blank session: %w", err)
	}
	tmp, err := os.CreateTemp("", "cmdr-blank-session-*.json")
	if err != nil {
		return "", fmt.Errorf("create temp session file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write temp session file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close temp session file: %w", err)
	}

	cmd := exec.CommandContext(ctx, "opencode", "import", tmp.Name())
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: opencode import: %w: %s", ErrOpencodeUnavailable, err, strings.TrimSpace(string(out)))
	}
	importedID, err := parseImportedSessionID(string(out))
	if err != nil {
		return "", fmt.Errorf("%w: opencode import: %w", ErrOpencodeUnavailable, err)
	}
	return importedID, nil
}

type commanderBlankSession struct {
	Info     commanderBlankSessionInfo `json:"info"`
	Messages []any                     `json:"messages"`
}

type commanderBlankSessionInfo struct {
	ID         string                        `json:"id"`
	Slug       string                        `json:"slug"`
	ProjectID  string                        `json:"projectID"`
	Directory  string                        `json:"directory"`
	Path       string                        `json:"path"`
	Title      string                        `json:"title"`
	Version    string                        `json:"version"`
	Summary    commanderBlankSessionSummary  `json:"summary"`
	Permission []commanderBlankSessionPerm   `json:"permission"`
	Time       commanderBlankSessionInfoTime `json:"time"`
}

type commanderBlankSessionSummary struct {
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
	Files     int `json:"files"`
}

type commanderBlankSessionPerm struct {
	Permission string `json:"permission"`
	Pattern    string `json:"pattern"`
	Action     string `json:"action"`
}

type commanderBlankSessionInfoTime struct {
	Created int64 `json:"created"`
	Updated int64 `json:"updated"`
}

func marshalCommanderBlankSession(sessionID, workspaceRoot string, now time.Time) ([]byte, error) {
	path := strings.TrimPrefix(filepathCleanSlash(workspaceRoot), "/")
	created := now.UnixMilli()
	return json.MarshalIndent(commanderBlankSession{
		Info: commanderBlankSessionInfo{
			ID:        sessionID,
			Slug:      "commander",
			ProjectID: "global",
			Directory: workspaceRoot,
			Path:      path,
			Title:     "Commander",
			Version:   "0",
			Summary: commanderBlankSessionSummary{
				Additions: 0,
				Deletions: 0,
				Files:     0,
			},
			Permission: []commanderBlankSessionPerm{
				{Permission: "question", Pattern: "*", Action: "deny"},
				{Permission: "plan_enter", Pattern: "*", Action: "deny"},
				{Permission: "plan_exit", Pattern: "*", Action: "deny"},
			},
			Time: commanderBlankSessionInfoTime{
				Created: created,
				Updated: created,
			},
		},
		Messages: []any{},
	}, "", "  ")
}

func filepathCleanSlash(path string) string {
	return strings.ReplaceAll(path, "\\", "/")
}

func newCommanderSessionID() string {
	return fmt.Sprintf("ses_cmdr_%d", time.Now().UnixNano())
}

func parseImportedSessionID(out string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Imported session:") {
			continue
		}
		id := strings.TrimSpace(strings.TrimPrefix(line, "Imported session:"))
		if id == "" {
			break
		}
		return id, nil
	}
	return "", fmt.Errorf("missing imported session id in output %q", strings.TrimSpace(out))
}
