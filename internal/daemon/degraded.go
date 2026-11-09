package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/mailstate"
)

// When cmdrd cannot read its state, the check-in must say so instead of
// reporting "all on track". The alert cannot go through state.db (role->name
// mapping lives there), so it uses a route cached while the db was readable
// and the am server's HTTP API directly. A file in $COMMANDER_HOME records the
// condition even when mail fails too.
const degradedRepeat = 30 * time.Minute

// mailRoute is the cached am addressing: project key plus the am names of the
// sender (cmdrd) and Commander.
type mailRoute struct {
	Key       string `json:"key"`
	Sender    string `json:"sender"`
	Commander string `json:"commander"`
}

type degradedSender interface {
	SendMessage(ctx context.Context, projectKey, sender string, to []string, subject, body string, opts agentmail.SendOptions) error
}

type roleNames interface {
	GetAmAgent(ctx context.Context, amProjectKey, role string) (string, error)
}

// degradedAlert raises the alarm at most once per degradedRepeat.
type degradedAlert struct {
	home   string
	mail   degradedSender // nil: file only
	names  roleNames      // the store, read while it still works
	key    string
	now    func() time.Time
	mu     sync.Mutex
	lastAt time.Time
}

func (a *degradedAlert) routePath() string    { return filepath.Join(a.home, "mail-route.json") }
func (a *degradedAlert) degradedPath() string { return filepath.Join(a.home, "degraded.json") }

// refreshRoute caches the am names while state is readable.
func (a *degradedAlert) refreshRoute(ctx context.Context) {
	if a == nil || a.names == nil {
		return
	}
	sender, err1 := a.names.GetAmAgent(ctx, a.key, "cmdrd")
	cmdr, err2 := a.names.GetAmAgent(ctx, a.key, commanderRole)
	if err1 != nil || err2 != nil || sender == "" || cmdr == "" {
		return
	}
	b, _ := json.Marshal(mailRoute{Key: a.key, Sender: sender, Commander: cmdr})
	_ = os.WriteFile(a.routePath(), b, 0o600)
}

// healthy clears a recorded degraded condition.
func (a *degradedAlert) healthy() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.lastAt = time.Time{}
	a.mu.Unlock()
	_ = os.Remove(a.degradedPath())
}

// raise records the condition and mails Commander (urgent) without the db.
func (a *degradedAlert) raise(ctx context.Context, reason string) {
	if a == nil {
		return
	}
	now := a.now()
	log.Printf("daemon: DEGRADED: %s", reason)
	b, _ := json.Marshal(map[string]any{"reason": reason, "since": now.Unix()})
	_ = os.WriteFile(a.degradedPath(), b, 0o600)

	a.mu.Lock()
	if !a.lastAt.IsZero() && now.Sub(a.lastAt) < degradedRepeat {
		a.mu.Unlock()
		return
	}
	a.lastAt = now
	a.mu.Unlock()

	if err := a.sendMail(ctx, reason); err != nil {
		log.Printf("daemon: DEGRADED alert not mailed: %v", err)
	}
}

func (a *degradedAlert) sendMail(ctx context.Context, reason string) error {
	if a.mail == nil {
		return fmt.Errorf("no mail client")
	}
	raw, err := os.ReadFile(a.routePath())
	if err != nil {
		return fmt.Errorf("no cached mail route: %w", err)
	}
	var r mailRoute
	if err := json.Unmarshal(raw, &r); err != nil || r.Key == "" || r.Sender == "" || r.Commander == "" {
		return fmt.Errorf("bad cached mail route")
	}
	subject := "cmdrd DEGRADED: cannot read state"
	body := "cmdrd cannot read its state, so check-ins and the watch are blind (they are NOT 'all on track'):\n\n" + reason +
		"\n\nTell the user. Likely cause: the state.db schema is newer than the installed cmdr/cmdrd (install the newer build), or the db is locked or missing."
	if err := a.mail.SendMessage(ctx, r.Key, r.Sender, []string{r.Commander}, subject, body, agentmail.SendOptions{Importance: "urgent"}); err != nil {
		return err
	}
	mailstate.Signal("commander")
	return nil
}
