// Package rolemail sends agent mail in role terms ("manager-commander-il9")
// while the am server only knows its own random names ("GoldTiger"). The role
// to name map lives in state.db (am_agents). See docs/agent-mail-routing.md.
package rolemail

import (
	"context"
	"fmt"
	"strings"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/mailstate"
	"github.com/example-user/commander/internal/store"
)

// Store is the slice of *store.Store that maps roles to am names.
type Store interface {
	GetAmAgent(ctx context.Context, amProjectKey, role string) (string, error)
	UpsertAmAgent(ctx context.Context, amProjectKey, role, amName string) error
}

// Lister is the optional reverse-lookup side of a Store (*store.Store has it).
type Lister interface {
	ListAmAgents(ctx context.Context, amProjectKey string) ([]store.AmAgent, error)
}

// Mailer is the slice of *agentmail.ServerClient used to send.
type Mailer interface {
	EnsureProject(ctx context.Context, humanKey string) (string, error)
	RegisterAgent(ctx context.Context, projectKey, program, model, task string) (string, error)
	SendMessage(ctx context.Context, projectKey, sender string, to []string, subject, body string, opts agentmail.SendOptions) error
}

// Registered returns the role spelling actually registered for role and its am
// name; both are empty when nothing matches. An exact match wins; for an
// Operator role every registered operator-* role with the same canonical
// spelling (spawn.CanonicalRole) counts, found by listing the registrations
// (a local sqlite read, made only when the exact role is not registered).
func Registered(ctx context.Context, st Store, amProjectKey, role string) (string, string, error) {
	name, err := st.GetAmAgent(ctx, amProjectKey, role)
	if err != nil {
		return "", "", err
	}
	if name != "" {
		return role, name, nil
	}
	lister, ok := st.(Lister)
	if !ok || !strings.HasPrefix(role, "operator-") {
		return "", "", nil
	}
	agents, err := lister.ListAmAgents(ctx, amProjectKey)
	if err != nil {
		return "", "", err
	}
	want := beads.OwnerLabelName(role)
	for _, a := range agents {
		if strings.HasPrefix(a.Role, "operator-") && beads.OwnerLabelName(a.Role) == want {
			return a.Role, a.AmName, nil
		}
	}
	return "", "", nil
}

// Resolve maps a role to its am name. An unmapped role is registered when
// autoRegister is set (the sender, an inbox owner); otherwise it is an error
// (recipients must exist).
//
// An Operator role is matched by either spelling (dots of a bead id or dashes)
// against the registered one, and never auto-registers a second agent for a
// spelling that matches an existing registration.
func Resolve(ctx context.Context, st Store, m Mailer, amProjectKey, role string, autoRegister bool) (string, error) {
	_, name, err := Registered(ctx, st, amProjectKey, role)
	if err != nil {
		return "", err
	}
	if name != "" {
		return name, nil
	}
	if !autoRegister {
		return "", fmt.Errorf("no am agent registered for role %q", role)
	}
	if _, err := m.EnsureProject(ctx, amProjectKey); err != nil {
		return "", fmt.Errorf("ensure am project: %w", err)
	}
	name, err = m.RegisterAgent(ctx, amProjectKey, "opencode-ai", "sonnet", "agent for "+role)
	if err != nil {
		return "", fmt.Errorf("register am agent for %q: %w", role, err)
	}
	if err := st.UpsertAmAgent(ctx, amProjectKey, role, name); err != nil {
		return "", fmt.Errorf("map %q: %w", role, err)
	}
	return name, nil
}

// Send resolves the from/to roles to am names and sends through the server.
func Send(ctx context.Context, st Store, m Mailer, amProjectKey, fromRole string, toRoles []string, subject, body string, opts agentmail.SendOptions) error {
	from, err := Resolve(ctx, st, m, amProjectKey, fromRole, true)
	if err != nil {
		return fmt.Errorf("resolve sender: %w", err)
	}
	to := make([]string, 0, len(toRoles))
	for _, r := range toRoles {
		name, err := Resolve(ctx, st, m, amProjectKey, r, false)
		if err != nil {
			return fmt.Errorf("resolve recipient: %w", err)
		}
		to = append(to, name)
	}
	if err := m.SendMessage(ctx, amProjectKey, from, to, subject, body, opts); err != nil {
		return err
	}
	// signal the spelling the Waker and the mail hook check: the registered one
	signal := make([]string, len(toRoles))
	for i, r := range toRoles {
		signal[i] = r
		if reg, _, err := Registered(ctx, st, amProjectKey, r); err == nil && reg != "" {
			signal[i] = reg
		}
	}
	mailstate.Signal(signal...)
	return nil
}
