package daemon

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/store"
)

// managerSleeper stops a Manager whose chat has sat idle a while, to give its
// memory back. In an invented capacity exercise, ten idle Managers retain ten
// chat processes unnecessarily. Nothing is lost: mail starts the same conversation
// (chatDeliverer.Deliver), and so does opening its chat in the app. Commander
// itself never sleeps.
type managerSleeper struct {
	chat interface {
		List() []chat.Status
		Talking(role string) bool
		Stop(ctx context.Context, role string) error
	}
	states func(ctx context.Context) ([]store.AgentState, error)
	unread func(ctx context.Context, role string) (int, error)
	idle   time.Duration
	every  time.Duration
	now    func() time.Time
}

// managerIdleBeforeSleep is how long a Manager stays idle before it sleeps.
const managerIdleBeforeSleep = 15 * time.Minute

func (s managerSleeper) Run(ctx context.Context) {
	t := time.NewTicker(s.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sweep(ctx)
		}
	}
}

// sweep puts to sleep every Manager that is running, idle past s.idle, not
// being talked to and with no unread mail; it returns the roles it stopped.
func (s managerSleeper) sweep(ctx context.Context) []string {
	states, err := s.states(ctx)
	if err != nil {
		return nil
	}
	since := map[string]store.AgentState{}
	for _, a := range states {
		since[a.Role] = a
	}
	var slept []string
	for _, st := range s.chat.List() {
		role := st.Role
		if !strings.HasPrefix(role, "manager-") || !st.Running || st.State != "idle" {
			continue
		}
		a, ok := since[role]
		if !ok || a.State != store.AgentIdle || s.now().Sub(time.Unix(a.Since, 0)) < s.idle {
			continue
		}
		if s.chat.Talking(role) {
			continue
		}
		if s.unread != nil {
			if n, err := s.unread(ctx, role); err != nil || n > 0 {
				continue
			}
		}
		if err := s.chat.Stop(ctx, role); err != nil {
			log.Printf("daemon: sleep %s: %v", role, err)
			continue
		}
		log.Printf("daemon: %s idle %s: asleep until mail or its chat opens", role, s.now().Sub(time.Unix(a.Since, 0)).Round(time.Minute))
		slept = append(slept, role)
	}
	return slept
}
