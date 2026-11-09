// Package inbox provides a background poller that drains an am inbox into a
// channel of Notification values for downstream UI consumers.
//
// The poller is intentionally narrow: it does not de-dupe, persist, or render.
// Callers (e.g. the Commander TUI) own those concerns. The contract is:
//
//   - Start launches a goroutine that wakes on a ticker and calls am.Inbox
//     for messages received since the last successful poll.
//   - Each fresh Mail is wrapped in a Notification and pushed to Out.
//   - When Out's buffer is full, the OLDEST notification is dropped to make
//     room for the newest. We prefer "freshness" over "completeness" — a
//     full buffer means the consumer is already behind and ancient mail is
//     the least useful thing to retain.
//   - Stop cancels the goroutine, waits for it, and closes Out.
package inbox

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/example-user/commander/internal/agentmail"
)

// Notification is the projection of an am Mail the TUI alerts surface needs.
// It is intentionally a value type (no pointers) so it can be passed across
// the channel without aliasing concerns.
type Notification struct {
	From, To, Subject, Body, Importance string
	SentAt                              time.Time
	Project                             string
}

// Poller runs an am.Inbox poll loop in a background goroutine. Construct via
// NewPoller, drive lifecycle with Start/Stop, and read freshly-arrived
// notifications off Out.
type Poller struct {
	am       agentmail.Client
	project  string
	agent    string
	interval time.Duration
	since    time.Time
	Out      chan Notification
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// NewPoller constructs a Poller. The Out channel is buffered (32) so brief
// consumer stalls don't block the poll loop. since is initialized to "now"
// so the first tick only surfaces mail that arrives during the poller's
// lifetime — historical mail isn't re-played on startup.
func NewPoller(am agentmail.Client, project, agent string, interval time.Duration) *Poller {
	return &Poller{
		am:       am,
		project:  project,
		agent:    agent,
		interval: interval,
		since:    time.Now(),
		Out:      make(chan Notification, 32),
	}
}

// Start launches the poll goroutine. Calling Start more than once is not
// supported; do that and you'll leak the previous goroutine.
func (p *Poller) Start(ctx context.Context) {
	cctx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.wg.Add(1)
	go p.loop(cctx)
}

// Stop cancels the poll goroutine, waits for it to exit, and closes Out.
// Safe to call after Start; calling Stop without Start is a no-op-with-close.
func (p *Poller) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
	close(p.Out)
}

// loop is the poll goroutine. It ticks at p.interval, pulls fresh mail via
// am.Inbox, and pushes each as a Notification onto Out. Errors are logged
// (not propagated) so a transient am hiccup doesn't kill the loop.
func (p *Poller) loop(ctx context.Context) {
	defer p.wg.Done()
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			mails, err := p.am.Inbox(ctx, p.project, p.agent, p.since, agentmail.InboxOptions{
				IncludeBodies: true,
				Limit:         32,
			})
			if err != nil {
				log.Printf("inbox poll err: %v", err)
				continue
			}
			// Advance the cursor BEFORE pushing — a downstream-block on Out
			// must not cause us to re-fetch the same mail next tick.
			p.since = time.Now()
			for _, m := range mails {
				n := Notification{
					From:       m.From,
					To:         m.To,
					Subject:    m.Subject,
					Body:       m.Body,
					Importance: m.Importance,
					SentAt:     m.SentAt,
					Project:    p.project,
				}
				select {
				case p.Out <- n:
				default:
					// Buffer full: drop the oldest, push the newest.
					// Freshness > completeness when the consumer is behind.
					select {
					case <-p.Out:
					default:
					}
					select {
					case p.Out <- n:
					default:
					}
				}
			}
		}
	}
}
