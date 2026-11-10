package wake

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/example-user/commander/internal/layout"
	"github.com/example-user/commander/internal/store"
)

// ProjectSource is the slice of *store.Store the tmux deliverer needs.
type ProjectSource interface {
	ListProjects(ctx context.Context) ([]store.Project, error)
	ListAgentRunsByProject(ctx context.Context, projectID int64, status string) ([]store.AgentRun, error)
}

// WorkstreamManagers opens (without focusing) the Manager for a mail role and
// returns its chat pane. *wsmgr.Manager satisfies it.
type WorkstreamManagers interface {
	OpenByRole(ctx context.Context, role string) (string, error)
}

// TmuxDeliverer opens the agent's window if needed and types into its chat pane.
type TmuxDeliverer struct {
	Projects ProjectSource
	Director *layout.Director
	Managers WorkstreamManagers
	// Probe gates every send-keys. Nil means a Probe on the real tmux.
	Probe *Probe
}

func (t *TmuxDeliverer) Deliver(ctx context.Context, role, prompt string) error {
	pane, err := t.chatPane(ctx, role)
	if err != nil {
		return err
	}
	if t.Probe == nil {
		t.Probe = NewProbe()
	}
	// Never type into a pane that is mid-turn or has the user's half-written
	// input in it; the Waker retries on its next poll.
	if err := t.Probe.Check(ctx, pane); err != nil {
		return err
	}
	return t.Director.SendToPane(ctx, pane, prompt)
}

func (t *TmuxDeliverer) chatPane(ctx context.Context, role string) (string, error) {
	switch {
	case role == "commander":
		return t.Director.CommanderChatPane(ctx)
	case strings.HasPrefix(role, "manager-"):
		// Workstream Managers only: there is no per-project alias any more.
		if t.Managers == nil {
			return "", fmt.Errorf("no workstream Manager resolver for role %q", role)
		}
		return t.Managers.OpenByRole(ctx, role)
	case strings.HasPrefix(role, "operator-"):
		// A role comes in either spelling of its run's AgentName (as receives
		// reads it); a row from before host Operators is named by its task.
		dashed := strings.ReplaceAll(role, ".", "-")
		flat := strings.TrimPrefix(dashed, "operator-")
		projects, err := t.Projects.ListProjects(ctx)
		if err != nil {
			return "", err
		}
		for _, p := range projects {
			runs, err := t.Projects.ListAgentRunsByProject(ctx, p.ID, "running")
			if err != nil {
				return "", err
			}
			for _, r := range runs {
				if strings.ReplaceAll(r.AgentName, ".", "-") == dashed || layout.FlattenName(r.TaskBeadID) == flat {
					if r.TmuxPaneID.Valid && r.TmuxPaneID.String != "" {
						// A host Operator (its run row names its session on the ops
						// socket): it has no cockpit window and is never typed into.
						// Its mail hook takes the mail at its next tool call.
						return "", ErrHostOperator
					}
					if pr, ok := t.Projects.(PlaceRuns); ok {
						// A run on a paired place (the user's Mac) has no pane
						// here either: its mail hook asks cmdrd at each tool
						// call. Opening a window would start a second, local
						// Operator on the same task.
						run, err := pr.GetPlaceRun(ctx, r.ID)
						if err != nil {
							return "", err
						}
						if run != nil {
							return "", ErrPlaceOperator
						}
					}
					return t.Director.EnsureOperatorChatPane(ctx, r.TaskBeadID, p.Name, p.Path)
				}
			}
		}
		return "", fmt.Errorf("%w for role %q", ErrOperatorGone, role)
	}
	return "", fmt.Errorf("role %q is not wakeable", role)
}

// ErrHostOperator is ErrDeferred for a host Operator: mail is never typed into
// it and needs no tmux; the mail hook shows it at the Operator's next tool call
// or stop (an Operator inside a long tool call sees it when that call returns).
// A Codex one-shot has no such hook: its mail stays unread and is reported.
var ErrHostOperator = fmt.Errorf("%w: host Operator reads its mail at its next tool call (mid long tool call: when it returns)", ErrDeferred)

// PlaceRuns is the optional part of a ProjectSource that knows place runs
// (*store.Store has it).
type PlaceRuns interface {
	GetPlaceRun(ctx context.Context, runID int64) (*store.PlaceRun, error)
}

// ErrPlaceOperator is ErrHostOperator for an Operator on a paired place: its
// mail is never typed anywhere; the Mac's mail hook reads it from cmdrd.
var ErrPlaceOperator = fmt.Errorf("%w (on a paired place)", ErrHostOperator)

// ErrOperatorGone means the role is an Operator whose run has ended: its mail
// has nobody to wake, so the waker drops it instead of retrying forever.
var ErrOperatorGone = fmt.Errorf("no running operator")

// ErrStuck means mail is waiting but the input box holds text that is not a
// stray keystroke, so it was left alone: a person may be mid-message.
var ErrStuck = fmt.Errorf("input box has text")

// maxStrayRunes is the longest input box content treated as a stray keystroke
// (like a 'q' that leaked in) rather than a message being written.
const maxStrayRunes = 12

// DeliverStale is Deliver for mail that has waited too long. A short, untouched
// input is a stray: it is cleared (Ctrl-U), the wake line is sent, and the text is
// put back so nothing is lost. Anything longer returns ErrStuck.
func (t *TmuxDeliverer) DeliverStale(ctx context.Context, role, prompt string) error {
	pane, err := t.chatPane(ctx, role)
	if err != nil {
		return err
	}
	if t.Probe == nil {
		t.Probe = NewProbe()
	}
	err = t.Probe.Check(ctx, pane)
	var busy *InputBusyError
	if err == nil {
		return t.Director.SendToPane(ctx, pane, prompt)
	}
	if !errors.As(err, &busy) {
		return err
	}
	if len([]rune(busy.Text)) > maxStrayRunes || strings.Contains(busy.Text, "\n") || busy.StableFor < QuietPeriod {
		return ErrStuck
	}
	if err := t.Director.ClearInput(ctx, pane); err != nil {
		return err
	}
	if err := t.Director.SendToPane(ctx, pane, prompt); err != nil {
		return err
	}
	return t.Director.TypeInto(ctx, pane, busy.Text) // restore what was there
}
