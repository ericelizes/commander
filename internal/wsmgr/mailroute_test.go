package wsmgr

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

type sentMail struct{ role, subject, body string }

func routeFixture(t *testing.T) (*fixture, *beads.MemClient, *[]sentMail, SendFunc) {
	t.Helper()
	f := newFixture(t)
	bc := &beads.MemClient{EdgeParentsOnly: true}
	bc.Seed(beads.Bead{ID: "demo-root", Status: "open", Labels: []string{"kind:objective"}})
	bc.Seed(beads.Bead{ID: "demo-root.2", Status: "open", Labels: []string{"kind:operational"}})
	bc.Seed(beads.Bead{ID: "demo-root.2.1", Status: "open", Labels: []string{"kind:task"}})
	bc.Seed(beads.Bead{ID: "demo-done", Status: "closed", Labels: []string{"kind:objective"}})
	bc.Seed(beads.Bead{ID: "demo-done.1", Status: beads.StatusDeferred, Labels: []string{"kind:objective"}})
	bc.Seed(beads.Bead{ID: "demo-loose", Status: "open", Labels: []string{"kind:task"}})
	bc.Edges = []beads.Dep{
		{IssueID: "demo-root.2", DependsOnID: "demo-root", Type: "parent-child"},
		{IssueID: "demo-root.2.1", DependsOnID: "demo-root.2", Type: "parent-child"},
		{IssueID: "demo-done.1", DependsOnID: "demo-done", Type: "parent-child"},
	}
	f.m.Beads = bc
	var sent []sentMail
	send := f.m.RoutedMail(func(_ context.Context, role, subject, body string) error {
		sent = append(sent, sentMail{role, subject, body})
		return nil
	})
	return f, bc, &sent, send
}

func TestRoutedMailFollowsRealAncestry(t *testing.T) {
	for _, tc := range []struct {
		name, role, to, inBody string
		marked                 bool
	}{
		{"open workstream unchanged", "manager-demo-root", "manager-demo-root", "", false},
		{"nested role goes to its root", "manager-demo-root.2.1", "manager-demo-root", "Routed to you by parent ownership", false},
		{"closed workstream has no owner", "manager-demo-done", "commander", "no open workstream owns it", true},
		{"nested under a closed root has no owner", "manager-demo-done.1", "commander", "no open workstream owns it", true},
		{"open non-objective is no workstream", "manager-demo-loose", "commander", "no open workstream owns it", true},
		{"role naming no bead is left alone", "manager-example-app", "manager-example-app", "", false},
		{"not a Manager is left alone", "operator-demo", "operator-demo", "", false},
		{"commander is left alone", "commander", "commander", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, sent, send := routeFixture(t)
			if err := send(context.Background(), tc.role, "[wait] event", "what happened"); err != nil {
				t.Fatal(err)
			}
			if len(*sent) != 1 {
				t.Fatalf("sent %+v, want exactly one mail: never lost, never doubled", *sent)
			}
			got := (*sent)[0]
			if got.role != tc.to {
				t.Fatalf("went to %s, want %s", got.role, tc.to)
			}
			if !strings.Contains(got.body, "what happened") || !strings.Contains(got.body, tc.inBody) {
				t.Fatalf("body %q lost the mail or the reason (%q)", got.body, tc.inBody)
			}
			if tc.marked != strings.HasPrefix(got.subject, "[no Manager] ") || !strings.Contains(got.subject, "[wait] event") {
				t.Fatalf("subject %q", got.subject)
			}
			if tc.marked && !strings.Contains(got.body, tc.role) {
				t.Fatalf("body %q does not name who it was for", got.body)
			}
		})
	}
}

type busyShows struct{ beads.Client }

func (busyShows) Show(context.Context, string, string) (*beads.Bead, error) {
	return nil, errors.New("busy")
}

// A failed read says nothing about the route: the mail goes as addressed, and
// the Waker's retained-mail alert still covers a dead role.
func TestRoutedMailKeepsTheAddressOnAFailedRead(t *testing.T) {
	f, bc, sent, send := routeFixture(t)
	f.m.Beads = busyShows{bc}
	if err := send(context.Background(), "manager-demo-done", "s", "b"); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 1 || (*sent)[0].role != "manager-demo-done" || (*sent)[0].subject != "s" {
		t.Fatalf("sent %+v", *sent)
	}
}

// Routing only reads: no Manager started, no registration or bead written.
func TestRoutedMailStartsAndRegistersNothing(t *testing.T) {
	f, bc, _, send := routeFixture(t)
	before := len(bc.Edges)
	for _, role := range []string{"manager-demo-done", "manager-demo-root.2.1", "manager-demo-root"} {
		if err := send(context.Background(), role, "s", "b"); err != nil {
			t.Fatal(err)
		}
		if row, _ := f.st.GetWorkstreamManager(context.Background(), f.proj.ID, strings.TrimPrefix(role, "manager-")); row != nil {
			t.Fatalf("registered %+v", row)
		}
	}
	if len(bc.Edges) != before {
		t.Fatal("edges changed")
	}
	if n, ok := f.m.Native.(*fakeNative); ok && len(n.started) > 0 {
		t.Fatalf("started %v", n.started)
	}
}

type blockedShows struct{ beads.Client }

func (blockedShows) Show(ctx context.Context, _, _ string) (*beads.Bead, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// Route reads run out on their own deadline: what is left is the send's, so the
// mail still goes, as addressed.
func TestRoutedMailReadsCannotSpendTheSendsDeadline(t *testing.T) {
	f, bc, _, _ := routeFixture(t)
	f.m.Beads = blockedShows{bc}
	var to string
	send := f.m.RoutedMail(func(ctx context.Context, role, _, _ string) error {
		to = role
		return ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := send(ctx, "manager-demo-done", "s", "b"); err != nil {
		t.Fatalf("the send got a spent context: %v", err)
	}
	if to != "manager-demo-done" {
		t.Fatalf("went to %s, want as addressed", to)
	}
}

// countedReads counts reads and fails the ones named: failShow[n], failDetails[n]
// for the nth call, from 1.
type countedReads struct {
	beads.Client
	shows, details        int
	failShow, failDetails map[int]bool
}

func (c *countedReads) Show(ctx context.Context, dir, id string) (*beads.Bead, error) {
	c.shows++
	if c.failShow[c.shows] {
		return nil, errors.New("busy")
	}
	return c.Client.Show(ctx, dir, id)
}

func (c *countedReads) Details(ctx context.Context, dir string, ids []string) (map[string]beads.Detail, error) {
	c.details++
	if c.failDetails[c.details] {
		return nil, errors.New("busy")
	}
	return c.Client.Details(ctx, dir, ids)
}

// Every read of the route can fail, and none of them may read as "no owner":
// that would send a live Manager's mail to Commander.
func TestRoutedMailKeepsTheAddressWhenALaterReadFails(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reads countedReads
	}{
		{"finding the project", countedReads{failShow: map[int]bool{1: true}}},
		{"StrictRoot after the bead was found", countedReads{failDetails: map[int]bool{1: true}}},
		{"StrictRoot inside validateTarget", countedReads{failDetails: map[int]bool{2: true}}},
		{"Show inside validateTarget", countedReads{failShow: map[int]bool{2: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, bc, sent, send := routeFixture(t)
			reads := tc.reads
			reads.Client = bc
			f.m.Beads = &reads
			owner, why, err := f.m.OwnerOfRole(context.Background(), "manager-demo-done")
			if err == nil || owner != "" || why != "" {
				t.Fatalf("owner=%q why=%q err=%v, want only the read's error", owner, why, err)
			}
			reads.shows, reads.details = 0, 0
			if err := send(context.Background(), "manager-demo-done", "s", "b"); err != nil {
				t.Fatal(err)
			}
			if len(*sent) != 1 || (*sent)[0] != (sentMail{"manager-demo-done", "s", "b"}) {
				t.Fatalf("sent %+v, want it untouched and as addressed", *sent)
			}
		})
	}
}

// What one routed mail costs, in br show processes (CLAUDE.md quotes this).
func TestOwnerOfRoleReadCount(t *testing.T) {
	for _, tc := range []struct {
		name, role string
		reads      int
	}{
		{"a root", "manager-demo-root", 4},
		{"one more per ancestor", "manager-demo-root.2.1", 6},
		{"a role naming no bead reads the one project", "manager-example-app", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, bc, _, _ := routeFixture(t)
			reads := &countedReads{Client: bc}
			f.m.Beads = reads
			if _, _, err := f.m.OwnerOfRole(context.Background(), tc.role); err != nil {
				t.Fatal(err)
			}
			if got := reads.shows + reads.details; got != tc.reads {
				t.Fatalf("%d reads (%d show, %d details), want %d", got, reads.shows, reads.details, tc.reads)
			}
		})
	}
}

// perDir answers each project from its own beads; a project mapped to nil is
// unreadable.
type perDir struct {
	beads.Client
	dirs map[string]*beads.MemClient
	asks []string
}

func (p *perDir) Show(ctx context.Context, dir, id string) (*beads.Bead, error) {
	p.asks = append(p.asks, dir)
	c := p.dirs[dir]
	if c == nil {
		return nil, errors.New("busy")
	}
	return c.Show(ctx, dir, id)
}

func (p *perDir) Details(ctx context.Context, dir string, ids []string) (map[string]beads.Detail, error) {
	c := p.dirs[dir]
	if c == nil {
		return nil, errors.New("busy")
	}
	return c.Details(ctx, dir, ids)
}

func TestOwnerOfRoleAcrossProjects(t *testing.T) {
	objective := []string{"kind:objective"}
	first := func(bs ...beads.Bead) *beads.MemClient {
		c := &beads.MemClient{EdgeParentsOnly: true}
		for _, b := range bs {
			c.Seed(b)
		}
		return c
	}
	for _, tc := range []struct {
		name         string
		app, other   *beads.MemClient
		role, owner  string
		noOwner, bad bool
		firstAsked   string
	}{
		{name: "the bead lives in the second project", app: first(), other: first(beads.Bead{ID: "zed-open", Status: "open", Labels: objective}),
			role: "manager-zed-open", owner: "manager-zed-open"},
		{name: "the project its id is prefixed with is asked first", app: first(beads.Bead{ID: "second-x", Status: "closed", Labels: objective}), other: first(beads.Bead{ID: "second-x", Status: "open", Labels: objective}),
			role: "manager-second-x", owner: "manager-second-x", firstAsked: "second"},
		{name: "a closed bead in a later project has no owner", app: first(), other: first(beads.Bead{ID: "zed-done", Status: "closed", Labels: objective}),
			role: "manager-zed-done", noOwner: true},
		{name: "an unreadable project does not hide a hit elsewhere", other: first(beads.Bead{ID: "zed-open", Status: "open", Labels: objective}),
			role: "manager-zed-open", owner: "manager-zed-open"},
		{name: "an unreadable project with no hit elsewhere says nothing about the route", other: first(),
			role: "manager-zed-open", bad: true},
		{name: "no project has it: a project-level Manager", app: first(), other: first(),
			role: "manager-zed-open", owner: "manager-zed-open"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			otherPath := t.TempDir()
			if _, err := f.st.InsertProject(context.Background(), store.Project{Name: "second", Type: "code", Path: otherPath, ActivatedAt: 1, Status: "active"}); err != nil {
				t.Fatal(err)
			}
			reads := &perDir{dirs: map[string]*beads.MemClient{f.proj.Path: tc.app, otherPath: tc.other}}
			f.m.Beads = reads
			owner, why, err := f.m.OwnerOfRole(context.Background(), tc.role)
			if tc.bad != (err != nil) {
				t.Fatalf("err=%v, want failed read=%v", err, tc.bad)
			}
			if owner != tc.owner || tc.noOwner != (why != "") {
				t.Fatalf("owner=%q why=%q, want owner %q, no owner=%v", owner, why, tc.owner, tc.noOwner)
			}
			if tc.firstAsked == "second" && reads.asks[0] != otherPath {
				t.Fatalf("asked %v first, want the prefixed project", reads.asks)
			}
		})
	}
}
