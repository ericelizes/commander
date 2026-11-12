package livedata

import (
	"context"
	"github.com/example-user/commander/internal/binstage"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/mailstate"
	"github.com/example-user/commander/internal/store"
)

// UnreadFunc returns the unread mail count for a mail role.
type UnreadFunc func(ctx context.Context, role string) (int, error)

// UnreadListFunc returns the unread mail for a mail role, oldest first.
type UnreadListFunc func(ctx context.Context, role string) ([]agentmail.Mail, error)

// NewUnreadListFunc lists unread mail for a role, like NewUnreadFunc counts it.
func NewUnreadListFunc(st *store.Store) UnreadListFunc {
	return newUnreadListFunc(st, false)
}

// NewUnreadBodyListFunc includes the instructions delivered to an agent hook.
func NewUnreadBodyListFunc(st *store.Store) UnreadListFunc {
	return newUnreadListFunc(st, true)
}

func newUnreadListFunc(st *store.Store, bodies bool) UnreadListFunc {
	if st == nil {
		return nil
	}
	key := FleetMailKey()
	return func(ctx context.Context, role string) ([]agentmail.Mail, error) {
		client, err := agentmail.NewServerClientFromEnv()
		if err != nil {
			return nil, err
		}
		inbox := mailstate.FetchFunc(func(ctx context.Context, role string) ([]agentmail.Mail, error) {
			name, err := st.GetAmAgent(ctx, key, role)
			if err != nil || name == "" {
				return nil, err
			}
			return client.FetchInbox(ctx, key, name, time.Time{}, agentmail.InboxOptions{Limit: 50, IncludeBodies: bodies})
		})
		mails, err := mailstate.Unread(ctx, inbox, role)
		if err != nil || len(mails) == 0 {
			return mails, err
		}
		// The mail server names agents itself (BlueLake); the app shows a role.
		if agents, lerr := st.ListAmAgents(ctx, key); lerr == nil {
			roleOf := make(map[string]string, len(agents))
			for _, a := range agents {
				roleOf[a.AmName] = a.Role
			}
			for i := range mails {
				if r, ok := roleOf[mails[i].From]; ok {
					mails[i].From = r
				}
			}
		}
		return mails, nil
	}
}

// NewUnreadFunc counts unread mail for a role through the mail server and the
// store's role -> am name mapping. The mail client is built per call so a
// server that starts after the caller does is picked up on the next call.
func NewUnreadFunc(st *store.Store) UnreadFunc {
	if st == nil {
		return nil
	}
	key := FleetMailKey()
	return func(ctx context.Context, role string) (int, error) {
		client, err := agentmail.NewServerClientFromEnv()
		if err != nil {
			return 0, err
		}
		inbox := mailstate.FetchFunc(func(ctx context.Context, role string) ([]agentmail.Mail, error) {
			name, err := st.GetAmAgent(ctx, key, role)
			if err != nil || name == "" {
				return nil, err // a role with no mailbox yet has no mail
			}
			return client.FetchInbox(ctx, key, name, time.Time{}, agentmail.InboxOptions{Limit: 50})
		})
		return mailstate.Count(ctx, inbox, role)
	}
}

// FleetMailKey is the fleet's agent-mail project key: $COMMANDER_REPO, else
// the commander checkout the running binary was built in, else
// ~/code/commander. It must match cmd/cmdr's resolveCommanderRepo.
func FleetMailKey() string {
	if v := os.Getenv("COMMANDER_REPO"); v != "" {
		return v
	}
	if exe, err := os.Executable(); err == nil {
		if repo := binstage.RepoHint(exe); repo != "" {
			return repo
		}
		if root, ok := commanderRootFrom(filepath.Dir(exe)); ok {
			return root
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "code", "commander")
	}
	return ""
}

// commanderRootFrom walks up from start looking for the commander go.mod.
func commanderRootFrom(start string) (string, bool) {
	cur := start
	for i := 0; i < 10; i++ {
		if body, err := os.ReadFile(filepath.Join(cur, "go.mod")); err == nil &&
			strings.Contains(string(body), "module github.com/example-user/commander") {
			return cur, true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false
		}
		cur = parent
	}
	return "", false
}
