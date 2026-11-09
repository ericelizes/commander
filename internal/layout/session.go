package layout

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/example-user/commander/internal/agentrt"
	promptassets "github.com/example-user/commander/prompts"
)

// CommanderSessionFileName is the file under ~/.commander/ that stores the
// Commander tier opencode session id. Persisted on first capture; subsequent
// `cmdr` launches just resume.
const CommanderSessionFileName = "commander.session-id"

// CaptureFn is the function signature `EnsureCommanderSessionID` uses to
// mint a fresh opencode session. Production code passes an implementation
// that runs `opencode run --format json` once and parses the captured id off
// the first nd-JSON event. Tests inject a fake that returns a canned id.
type CaptureFn func(ctx context.Context) (string, error)

// EnsureCommanderSessionID returns the Commander session id, capturing one
// via captureFn and persisting it to <commanderHome>/commander.session-id if
// the file doesn't already exist. commanderHome may be "" — defaults to
// $HOME/.commander or $COMMANDER_HOME if set.
func EnsureCommanderSessionID(ctx context.Context, commanderHome string, capture CaptureFn) (string, error) {
	return ensureCommanderSessionIDFile(ctx, commanderHome, CommanderSessionFileName, capture)
}

// EnsureClaudeCommanderSessionID is EnsureCommanderSessionID for the Claude
// runtime: it installs the Claude commander agent and mints a UUID instead of
// importing a blank opencode session.
func EnsureClaudeCommanderSessionID(ctx context.Context, commanderHome string) (string, error) {
	return ensureCommanderSessionIDFile(ctx, commanderHome, claudeCommanderSessionFileName, func(context.Context) (string, error) {
		if err := promptassets.EnsureClaudeAgent(promptassets.RoleCommander, promptassets.CommanderAgentName); err != nil {
			return "", fmt.Errorf("ensure claude commander agent: %w", err)
		}
		return agentrt.NewClaudeSessionID(), nil
	})
}

var claudeCommanderSessionFileName = agentrt.SessionFileName("commander", agentrt.Claude)

func ensureCommanderSessionIDFile(ctx context.Context, commanderHome, fileName string, capture CaptureFn) (string, error) {
	path, err := commanderSessionPath(commanderHome, fileName)
	if err != nil {
		return "", err
	}

	if id, err := readSessionIDFile(path); err == nil && id != "" {
		return id, nil
	}

	if capture == nil {
		return "", fmt.Errorf("commander session id missing at %s and no capture fn provided", path)
	}

	id, err := capture(ctx)
	if err != nil {
		return "", fmt.Errorf("capture commander session: %w", err)
	}
	if id == "" {
		return "", fmt.Errorf("capture commander session: empty id returned")
	}

	if err := writeSessionIDFile(path, id); err != nil {
		// Persist failure is non-fatal for THIS launch — we have the id in
		// memory — but the next launch would re-capture, costing one
		// pointless opencode invocation. Surface the error so callers can
		// log it.
		return id, fmt.Errorf("write %s: %w", path, err)
	}
	return id, nil
}

// ClearCommanderSessionID removes the persisted Commander session-id file for
// the configured runtime so the next LaunchCommander call captures a fresh
// Commander brain.
func ClearCommanderSessionID(commanderHome string) error {
	fileName := CommanderSessionFileName
	if agentrt.For(agentrt.TierCommander) == agentrt.Claude {
		fileName = claudeCommanderSessionFileName
	}
	path, err := commanderSessionPath(commanderHome, fileName)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// commanderSessionPath returns the absolute path to the Commander tier
// session-id file. Honors $COMMANDER_HOME, then $HOME/.commander, then the
// supplied default.
func commanderSessionPath(commanderHome, fileName string) (string, error) {
	if commanderHome == "" {
		if v := os.Getenv("COMMANDER_HOME"); v != "" {
			commanderHome = v
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("resolve home dir: %w", err)
			}
			commanderHome = filepath.Join(home, ".commander")
		}
	}
	if err := os.MkdirAll(commanderHome, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", commanderHome, err)
	}
	return filepath.Join(commanderHome, fileName), nil
}

// readSessionIDFile reads + trims the contents of path. Missing file returns
// ("", nil) so callers can branch on emptiness; other errors propagate.
func readSessionIDFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// writeSessionIDFile writes id+newline atomically (write to a temp sibling +
// rename). Mirrors how internal/activation persists the Manager session id.
func writeSessionIDFile(path, id string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".session-id-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(id + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// captureCommanderSessionFn is the production CaptureFn. It is wired into
// LaunchCommander so a fresh machine on first `cmdr` launch captures a
// Commander tier session id by running `opencode run --format json` ONCE
// briefly, parsing the sessionID off the first nd-JSON event, and returning.
//
// The capture is intentionally minimal — we only need the id, not the
// response text — so we send a short role-specific Commander bootstrap.
//
// We DO NOT depend on internal/opencode here because internal/layout is
// imported by cmd/cmdr/main.go and we want the package to stay small. The
// caller can replace this with a richer implementation by passing a custom
// CaptureFn to EnsureCommanderSessionID.
var captureCommanderSessionFn CaptureFn = defaultCaptureCommanderSession

// SetCommanderCaptureFn replaces the package-level capture fn used by
// LaunchCommander when no session id file exists. Mostly for tests +
// integration points where the real opencode invocation isn't desired.
func SetCommanderCaptureFn(fn CaptureFn) {
	if fn == nil {
		captureCommanderSessionFn = defaultCaptureCommanderSession
		return
	}
	captureCommanderSessionFn = fn
}

// defaultCaptureCommanderSession is the production capture path. It defers
// to the cmd/cmdr binary's own opencode wrapper, which requires us to NOT
// import internal/opencode here. Implemented as a stub that errors with a
// helpful hint — cmd/cmdr/main.go calls SetCommanderCaptureFn at startup
// with a real implementation.
func defaultCaptureCommanderSession(ctx context.Context) (string, error) {
	return "", fmt.Errorf("commander session capture not configured: cmd/cmdr should call layout.SetCommanderCaptureFn at startup")
}
