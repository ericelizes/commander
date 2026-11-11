package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/layout"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/wake"
	"github.com/example-user/commander/internal/wsmgr"
)

// HandoverEnvVar enables chat Handover/PopOut through the API (each call must
// still pass confirm=true). Off by default: moving the user's live Commander and
// Managers between the TUI and the engine is his call.
const HandoverEnvVar = "COMMANDER_CHAT_HANDOVER"

func newChatEngine(home string, st *store.Store) *chat.Engine {
	return chat.New(chat.Options{
		Home:          home,
		Resolver:      chat.StoreResolver{Store: st, Home: home},
		States:        st,
		Costs:         chat.StoreCosts{Store: st},
		Tmux:          layout.DefaultRunner{},
		AllowHandover: os.Getenv(HandoverEnvVar) == "1",
		CrashRestart:  restartsOnCrash,
		Logf:          log.Printf,
	})
}

// restartsOnCrash: Commander and Managers come back on their own after a crash;
// Operators are respawned on their bead by settle instead.
func restartsOnCrash(role string) bool {
	return chat.IsCommanderRole(role) || strings.HasPrefix(role, "manager-")
}

// chatDeliverer routes wake-up mail to a role's native chat when the engine
// runs it, and to its tmux pane otherwise.
type chatDeliverer struct {
	chat nativeEngine
	tmux wake.Deliverer
	// managers validates current parent ownership before every Manager delivery
	// and starts its native chat if needed. Nil in tests that do not need it.
	managers managerEnsurer
}

// nativeEngine is the part of chat.Engine mail delivery uses (a fake in tests).
type nativeEngine interface {
	Running(role string) bool
	WaitRunning(ctx context.Context, role string, wait time.Duration) bool
	Talking(role string) bool
	DeliverMail(ctx context.Context, role, text string) error
	MailDelivered(role string)
}

// startWait bounds how long mail waits for a start already in flight.
var startWait = 20 * time.Second

type managerEnsurer interface {
	EnsureRole(ctx context.Context, role string) error
}

// nativeChats lets wsmgr start and message Managers on cmdrd's engine.
type nativeChats struct{ e *chat.Engine }

func (n nativeChats) Start(ctx context.Context, role string) error {
	_, err := n.e.Start(ctx, role, chat.StartOptions{})
	var owned *chat.OwnedError
	if errors.As(err, &owned) {
		return fmt.Errorf("%w: %w", wsmgr.ErrSessionHeld, err)
	}
	return err
}

func (n nativeChats) Fresh(role string) bool { return n.e.NeverStarted(role) }

func (n nativeChats) Send(ctx context.Context, role, text string) error {
	return n.e.Send(ctx, role, text)
}

func (c chatDeliverer) Deliver(ctx context.Context, role, prompt string) error {
	if c.chat != nil && c.managers != nil && strings.HasPrefix(role, "manager-") {
		if err := c.wakeManager(ctx, role); err != nil {
			return err
		}
	}
	if c.chat != nil && c.chat.Running(role) {
		if c.chat.Talking(role) {
			// the user is talking to it: the mail waits, and goes in as one batch.
			return wake.ErrDeferred
		}
		err := c.chat.DeliverMail(ctx, role, prompt)
		if err == nil {
			c.chat.MailDelivered(role)
		}
		if errors.Is(err, chat.ErrBusy) {
			// Mid-turn: the Stop mail hook delivers it when the turn ends.
			return wake.ErrDeferred
		}
		return err
	}
	return c.tmux.Deliver(ctx, role, prompt)
}

// wakeManager only starts a Manager through the workstream resolver: an engine
// start alone cannot validate whether a stale role still owns a workstream.
func (c chatDeliverer) wakeManager(ctx context.Context, role string) error {
	err := c.managers.EnsureRole(ctx, role)
	if err != nil {
		if errors.Is(err, wsmgr.ErrSessionHeld) {
			return &wake.BlockedError{Cause: err, Remedy: "Conversation belongs to another process. Ask its owner to release it or explicitly authorize a handover; automatic handover is disabled."}
		}
		if errors.Is(err, wsmgr.ErrInvalidRoute) {
			return &wake.BlockedError{Cause: err, Remedy: "Correct the registered Manager/mail recipient using authoritative parent ownership; retained unread mail needs an explicit disposition."}
		}
		return fmt.Errorf("cannot wake %s through its workstream: %w", role, err)
	}
	if c.chat.Running(role) || c.chat.WaitRunning(ctx, role, startWait) {
		return nil
	}
	return fmt.Errorf("%s is not running after its validated start; inspect the chat start failure", role)
}

// DeliverStale keeps the tmux deliverer's stale handling for TUI chats.
func (c chatDeliverer) DeliverStale(ctx context.Context, role, prompt string) error {
	if c.chat != nil && (c.chat.Running(role) || (c.managers != nil && strings.HasPrefix(role, "manager-"))) {
		return c.Deliver(ctx, role, prompt)
	}
	if sd, ok := c.tmux.(wake.StaleDeliverer); ok {
		return sd.DeliverStale(ctx, role, prompt)
	}
	return c.tmux.Deliver(ctx, role, prompt)
}
