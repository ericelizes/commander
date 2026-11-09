package agentmail

import (
	"context"
	"fmt"
)

// RoleNames maps a commander role to its server-assigned am name under an am
// project key. *store.Store satisfies it.
type RoleNames interface {
	GetAmAgent(ctx context.Context, amProjectKey, role string) (string, error)
	UpsertAmAgent(ctx context.Context, amProjectKey, role, amName string) error
}

// ServerRegistrar adapts the spawner's Register (on Client, which shells out to
// the `am` CLI with a bare project name the server does not know) to the mail
// server keyed by the absolute repo path Key. A role already mapped is reused
// (a respawn keeps its registration); otherwise the server assigns a name and
// the role is mapped to it, so `cmdr internal-mail --to operator-<flat>`
// resolves. The project argument is ignored: am keys projects by path. Every
// other Client method goes to the embedded Client.
type ServerRegistrar struct {
	Client
	Server AgentRegistrar
	Store  RoleNames
	Key    string
}

func (r *ServerRegistrar) Register(ctx context.Context, _, program, model, role string) error {
	if name, err := r.Store.GetAmAgent(ctx, r.Key, role); err != nil || name != "" {
		return err
	}
	if _, err := r.Server.EnsureProject(ctx, r.Key); err != nil {
		return fmt.Errorf("ensure am project: %w", err)
	}
	name, err := r.Server.RegisterAgent(ctx, r.Key, program, model, "agent for "+role)
	if err != nil {
		return fmt.Errorf("register am agent for %q: %w", role, err)
	}
	return r.Store.UpsertAmAgent(ctx, r.Key, role, name)
}
