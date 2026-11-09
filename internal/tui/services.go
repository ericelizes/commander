package tui

import (
	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/docker"
	"github.com/example-user/commander/internal/opencode"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tmuxctl"
)

// Services bundles every live backend the column renderers might dispatch
// to. Nil-valued entries mean fixture mode; the column degrades to mocked
// data.
//
// Post-pivot the column renderers are pure read-side: they query the store
// for cross-fleet / per-project / per-task state and render. Spawn,
// ceilings, and the inbox poller live in cmd/cmdrd's daemon process, which
// owns its own Services bundle.
type Services struct {
	Store    *store.Store
	OpenCode opencode.Client
	AM       agentmail.Client
	Beads    beads.Client
	Docker   docker.Client
	Tmux     tmuxctl.Client
}
