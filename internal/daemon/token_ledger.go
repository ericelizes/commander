package daemon

import (
	"context"
	"log"
	"time"

	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tokenledger"
)

// tokenLedgerEvery is how often the agents' logs are read into usage_turns.
const tokenLedgerEvery = 2 * time.Minute

// runTokenLedger keeps the Usage tab's per-agent token counts current: every
// tokenLedgerEvery it reads what the chat and Operator logs gained.
func (d *Daemon) runTokenLedger(ctx context.Context) {
	l := tokenledger.Ledger{
		Store: d.store,
		Home:  d.home,
		Projects: func(ctx context.Context) ([]string, error) {
			ps, err := d.store.ListProjects(ctx)
			if err != nil {
				return nil, err
			}
			out := make([]string, 0, len(ps))
			for _, p := range ps {
				out = append(out, p.Path)
			}
			return out, nil
		},
		Account: func(role string) string { return chat.ReadAccount(d.home, role) },
	}
	t := time.NewTicker(tokenLedgerEvery)
	defer t.Stop()
	for {
		if err := l.Scan(ctx); err != nil && ctx.Err() == nil {
			log.Printf("daemon: token ledger: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

var _ tokenledger.Store = (*store.Store)(nil)
