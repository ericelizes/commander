package wsmgr

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// SendFunc mails one role, the shape of cmdrd's watcher senders.
type SendFunc func(ctx context.Context, role, subject, body string) error

// OwnerOfRole answers who owns mail addressed to a Manager role, from real
// parent edges read fresh:
//   - the role itself when its bead is an open workstream, or when the role
//     names no bead in any project (a project-level Manager: not ours to judge);
//   - the nearest open workstream's Manager when the bead sits under one (the
//     root's while the project's nested switch is off; a closed nested
//     workstream's mail goes to the next open owner up);
//   - "" and why when no open workstream owns it (closed, or not an objective).
//
// A failed read is an error: it says nothing about the route. Nothing here
// writes, registers or starts a Manager.
func (m *Manager) OwnerOfRole(ctx context.Context, role string) (owner, why string, err error) {
	bead, ok := strings.CutPrefix(role, "manager-")
	if !ok || bead == "" {
		return role, "", nil
	}
	p, err := m.projectOfBead(ctx, bead)
	if err != nil {
		return "", "", err
	}
	if p == nil {
		return role, "", nil
	}
	root, err := StrictOwner(ctx, m.Beads, p.Path, bead)
	if err != nil {
		return "", "", err
	}
	if err := m.validateTarget(ctx, Target{Project: *p, BeadID: root}); err != nil {
		if errors.Is(err, ErrInvalidRoute) {
			return "", err.Error(), nil
		}
		return "", "", err
	}
	return Role(root), "", nil
}

// projectOfBead finds the project holding bead, trying the projects its id is
// prefixed with first. Nil means no project has it; an unreadable project with no
// hit elsewhere is an error, since the bead may be the one it could not read.
func (m *Manager) projectOfBead(ctx context.Context, bead string) (*store.Project, error) {
	projects, err := m.Store.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(projects, func(a, b store.Project) int {
		switch ap, bp := strings.HasPrefix(bead, a.Name+"-"), strings.HasPrefix(bead, b.Name+"-"); {
		case ap && !bp:
			return -1
		case bp && !ap:
			return 1
		}
		return 0
	})
	var unread error
	for i := range projects {
		b, err := beads.FreshShow(ctx, m.Beads, projects[i].Path, bead)
		switch {
		case err == nil && b != nil:
			return &projects[i], nil
		case err != nil && !errors.Is(err, beads.ErrNotFound) && unread == nil:
			unread = err
		}
	}
	return nil, unread
}

// RoutedMail wraps send so a watcher's mail reaches who really owns it. Watchers
// name a Manager from labels, parents or board rows that can outlive the
// workstream; mail to such a role sits unread and raises an invalid-route alert.
// A nested role goes to its nearest owner's Manager; with no owner Commander gets it,
// marked, so the mail keeps a visible disposition and no owner is guessed. A failed
// read leaves the address alone: the Waker's retained-mail alert still covers it.
// The reads get half of the time ctx has left, so slow reads cannot cost the send.
func (m *Manager) RoutedMail(send SendFunc) SendFunc {
	return func(ctx context.Context, role, subject, body string) error {
		readCtx, cancel := routeReadContext(ctx)
		owner, why, err := m.OwnerOfRole(readCtx, role)
		cancel()
		switch {
		case err != nil:
			log.Printf("wsmgr: mail route of %s unread, sending as addressed: %v", role, err)
			return send(ctx, role, subject, body)
		case owner == role:
			return send(ctx, role, subject, body)
		case owner != "":
			log.Printf("wsmgr: mail for %s routed to its owner %s: %s", role, owner, subject)
			return send(ctx, owner, subject, fmt.Sprintf("%s\n\n(Addressed to %s, which owns no workstream. Routed to you by parent ownership.)", body, role))
		}
		log.Printf("wsmgr: mail for %s has no owning Manager, sent to commander: %s (%s)", role, subject, why)
		return send(ctx, "commander", "[no Manager] "+subject,
			fmt.Sprintf("cmdrd had this for %s, but no open workstream owns it (%s). No Manager was started. Decide what it needs, or drop it.\n\n%s", role, why, body))
	}
}

// routeReadContext bounds a route's reads to half of the time ctx has left.
func routeReadContext(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, time.Until(deadline)/2)
}

// NoteOnWorkstream writes text as a comment on the workstream bead role
// manages (a usage move recorded for whoever reads the bead). A role naming no
// bead of a registered project writes nothing.
func (m *Manager) NoteOnWorkstream(ctx context.Context, role, text string) error {
	bead, ok := strings.CutPrefix(role, "manager-")
	if !ok || bead == "" {
		return nil
	}
	p, err := m.projectOfBead(ctx, bead)
	if err != nil || p == nil {
		return err
	}
	return m.Beads.AddComment(ctx, p.Path, bead, "cmdrd: "+text)
}
