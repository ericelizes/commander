package chat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/example-user/commander/internal/claudeacct"
)

// AccountFile holds the Claude account a role runs on; missing = default.
func AccountFile(home, role string) string { return claudeacct.RoleFile(home, role) }

// ReadAccount is role's Claude account: the account file's name when that
// account exists, else claudeacct.Default.
func ReadAccount(home, role string) string { return claudeacct.ForRole(home, role) }

// WriteAccount records role's Claude account.
func WriteAccount(home, role, name string) error {
	if !ValidRole(role) {
		return fmt.Errorf("chat: invalid role %q", role)
	}
	if !claudeacct.Exists(home, name) {
		return fmt.Errorf("chat: no Claude account %q (cmdr account add %s)", name, name)
	}
	if err := os.MkdirAll(filepath.Join(home, "chat"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(AccountFile(home, role), []byte(name+"\n"), 0o600); err != nil {
		return err
	}
	// A new account choice replaces one a usage move kept (WriteAccountIntent).
	if err := os.Remove(AccountIntentFile(home, role)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// AccountIntentFile holds the account a spawn's usage move took role off
// (the account file then names where it runs), so its next respawn goes back
// to that choice once it has usage. Any later WriteAccount removes it.
func AccountIntentFile(home, role string) string {
	return filepath.Join(home, "chat", role+".account-intent")
}

// ReadAccountIntent is role's kept account, "" when none or no longer an account.
func ReadAccountIntent(home, role string) string {
	raw, err := os.ReadFile(AccountIntentFile(home, role))
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(raw))
	if name == "" || !claudeacct.Exists(home, name) {
		return ""
	}
	return name
}

// WriteRunAccount records that role runs on name though it was headed for
// intended (a usage move): the account file names name, the intent keeps intended.
func WriteRunAccount(home, role, name, intended string) error {
	if err := WriteAccount(home, role, name); err != nil {
		return err
	}
	if intended == "" || intended == name {
		return nil
	}
	return os.WriteFile(AccountIntentFile(home, role), []byte(intended+"\n"), 0o600)
}

// AutoSwitchFile exists when role moves to another logged-in account by
// itself at a usage limit (off by default).
func AutoSwitchFile(home, role string) string { return filepath.Join(home, "chat", role+".autoswitch") }

// AutoSwitch reports whether role switches accounts by itself at a usage limit.
func AutoSwitch(home, role string) bool { return fileExists(AutoSwitchFile(home, role)) }

// SetAutoSwitch turns role's auto-switch on or off.
func SetAutoSwitch(home, role string, on bool) error {
	if !ValidRole(role) {
		return fmt.Errorf("chat: invalid role %q", role)
	}
	if !on {
		if err := os.Remove(AutoSwitchFile(home, role)); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Join(home, "chat"), 0o700); err != nil {
		return err
	}
	return os.WriteFile(AutoSwitchFile(home, role), nil, 0o600)
}

// WithAccount sets spec's Claude account from the role's account file.
func WithAccount(spec Spec, home string) Spec {
	spec.Account = ReadAccount(home, spec.Role)
	return spec
}

// SwitchOptions tunes a runtime or account switch.
type SwitchOptions struct {
	// Continue sends ContinueMessage after the restart, so the agent picks up
	// the turn a usage limit stopped.
	Continue bool
}

// ContinueMessage is the first message after a switch made to get past a usage limit.
const ContinueMessage = "[switch] your last turn stopped at a usage limit, so this chat moved to another account; continue where you left off"

// SetAccount moves role onto Claude account name (and onto Claude, if it ran
// on Codex). A running chat restarts there and resumes the same session: its
// transcript is copied into the account first (spawn). Refused mid-turn with
// ErrBusy, and for an account that is not logged in. A chat a terminal pane holds
// keeps running on default: panes ignore the role's account, which applies when
// the engine next starts the chat.
func (e *Engine) SetAccount(ctx context.Context, role, name string, o SwitchOptions) (Status, error) {
	if !ValidRole(role) {
		return Status{}, fmt.Errorf("chat: invalid role %q", role)
	}
	if !claudeacct.Exists(e.opts.Home, name) {
		return Status{}, fmt.Errorf("chat: no Claude account %q (cmdr account add %s)", name, name)
	}
	if !claudeacct.Get(e.opts.Home, name).LoggedIn {
		return Status{}, fmt.Errorf("chat: Claude account %s is not logged in: run %s", name, claudeacct.LoginCommand(e.opts.Home, name))
	}
	p, err := e.idleProcess(role, "account")
	if err != nil {
		return Status{}, err
	}
	if err := WriteAccount(e.opts.Home, role, name); err != nil {
		return Status{}, err
	}
	if err := WriteEffectiveRuntime(e.opts.Home, role, RuntimeClaude); err != nil {
		return Status{}, err
	}
	if err := claudeacct.Sync(e.opts.Home, name); err != nil {
		e.opts.Logf("chat: sync account %s: %v", name, err)
	}
	if p == nil || (p.spec.Runtime != RuntimeCodex && p.spec.Account == name) {
		return e.Status(role)
	}
	return e.restart(ctx, role, o)
}

// idleProcess is role's process when it may be restarted now: nil when it is
// not running, ErrBusy while a turn (or an approval) is in flight.
func (e *Engine) idleProcess(role, what string) (*process, error) {
	c, p, err := e.live(role)
	if err != nil && err != ErrNotRunning {
		return nil, err
	}
	if p == nil {
		return nil, nil
	}
	c.mu.Lock()
	busy := c.tracker.turn || len(c.tracker.order) > 0
	c.mu.Unlock()
	if busy {
		return nil, fmt.Errorf("%w: wait for the turn to end before switching %s", ErrBusy, what)
	}
	return p, nil
}

// restart stops and starts role on its recorded runtime and account.
func (e *Engine) restart(ctx context.Context, role string, o SwitchOptions) (Status, error) {
	if err := e.Stop(ctx, role); err != nil {
		return Status{}, err
	}
	st, err := e.Start(ctx, role, StartOptions{})
	if err != nil || !o.Continue {
		return st, err
	}
	if err := e.send(role, ContinueMessage, "mail"); err != nil {
		return st, err
	}
	return e.Status(role)
}

// projectsDir is where spec's Claude keeps its transcripts.
func (e *Engine) projectsDir(spec Spec) string {
	if spec.Account == "" || spec.Account == claudeacct.Default {
		return e.opts.ProjectsDir
	}
	return claudeacct.ProjectsDir(e.opts.Home, spec.Account)
}

// carryTranscript makes spec's account hold the newest transcript of its
// session (claudeacct.CarryTranscript), so --resume continues the same conversation.
func (e *Engine) carryTranscript(spec Spec) (string, error) {
	return claudeacct.CarryTranscript(e.opts.Home, e.opts.ProjectsDir, spec.SessionID, spec.Account)
}
