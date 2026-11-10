package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
)

// slowDoor is a write door that answers after delay: a caller whose deadline
// comes first sees "context deadline exceeded" (the door still applies it).
// It checks the deadline instead of sleeping, so the test runs at once.
type slowDoor struct {
	*beads.MockClient
	delay time.Duration
}

func (d slowDoor) answer(ctx context.Context) error {
	dl, ok := ctx.Deadline()
	if ok && time.Until(dl) < d.delay {
		return fmt.Errorf("write door: Post /api/write: %w", context.DeadlineExceeded)
	}
	return nil
}

func (d slowDoor) Create(ctx context.Context, dir, title string, o beads.CreateOptions) (string, error) {
	if err := d.answer(ctx); err != nil {
		return "", err
	}
	return d.MockClient.Create(ctx, dir, title, o)
}
func (d slowDoor) AddComment(ctx context.Context, dir, id, body string) error {
	if err := d.answer(ctx); err != nil {
		return err
	}
	return d.MockClient.AddComment(ctx, dir, id, body)
}
func (d slowDoor) DependOn(ctx context.Context, dir, a, b string) error {
	if err := d.answer(ctx); err != nil {
		return err
	}
	return d.MockClient.DependOn(ctx, dir, a, b)
}
func (d slowDoor) SetLabel(ctx context.Context, dir, id, label string, on bool) error {
	if err := d.answer(ctx); err != nil {
		return err
	}
	return d.MockClient.SetLabel(ctx, dir, id, label, on)
}

// The door's worst case is br's 40s plus settleWait 20s; the verbs once gave
// up after 15s (create) and 30s (comment, dep, label ...).
func TestBeadWriteVerbsOutwaitASlowDoor(t *testing.T) {
	cases := []struct {
		name  string
		delay time.Duration
		run   func(*testing.T) (int, string)
	}{
		{"create after 25s", 25 * time.Second, func(t *testing.T) (int, string) {
			stdout, stderr, read := withTempOutputFiles(t)
			code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "slow one"}, stdout, stderr)
			_, e := read()
			return code, e
		}},
		{"comment at the worst case", 61 * time.Second, func(t *testing.T) (int, string) {
			stdout, stderr, read := withTempOutputFiles(t)
			code := runInternalComment([]string{"--project", "manifest", "--bead", "obj-1", "--text", "note"}, nil, stdout, stderr)
			_, e := read()
			return code, e
		}},
		{"label at the worst case", 61 * time.Second, func(t *testing.T) (int, string) {
			stdout, stderr, read := withTempOutputFiles(t)
			code := runInternalLabel([]string{"--project", "manifest", "--bead", "obj-1", "add", "plain"}, stdout, stderr)
			_, e := read()
			return code, e
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setupObjectiveProject(t, slowDoor{MockClient: &beads.MockClient{CreateID: "obj-9"}, delay: c.delay})
			t.Setenv("COMMANDER_ROLE", "")
			if code, errOut := c.run(t); code != 0 {
				t.Fatalf("exit=%d: %s", code, errOut)
			}
		})
	}
}

// A create the door applied but whose answer was lost, or whose follow-up
// steps failed, still reports the bead: a caller that saw "failed" would
// create it again.
func TestCreateThatLandedNeverLooksFailed(t *testing.T) {
	t.Run("label strip failed", func(t *testing.T) {
		mock := &beads.MockClient{CreateID: "obj-1.4", CreateErr: fmt.Errorf("strip inherited labels: busy")}
		setupObjectiveProject(t, mock)
		stdout, stderr, read := withTempOutputFiles(t)
		code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "child", "--parent", "obj-1", "--waiting", "review:rev"}, stdout, stderr)
		out, errOut := read()
		if code != 0 || !strings.Contains(out, `"bead_id":"obj-1.4"`) || !strings.Contains(errOut, "warning") {
			t.Fatalf("exit=%d out=%s err=%s", code, out, errOut)
		}
	})
	t.Run("link failed", func(t *testing.T) {
		fastLinkRetries(t)
		setupObjectiveProject(t, failingDeps{&beads.MockClient{CreateID: "obj-1.5"}})
		stdout, stderr, read := withTempOutputFiles(t)
		code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "child", "--parent", "obj-1", "--deps", "obj-1.2"}, stdout, stderr)
		out, errOut := read()
		var res internalCreateObjectiveResult
		_ = json.Unmarshal([]byte(strings.TrimSpace(out)), &res)
		if code != exitUnlinked || res.BeadID != "obj-1.5" || res.Complete || len(res.Warnings) != 1 || len(res.Unlinked) != 1 {
			t.Fatalf("exit=%d out=%s err=%s", code, out, errOut)
		}
	})
	t.Run("answer lost but bead exists", func(t *testing.T) {
		mock := &beads.MockClient{
			CreateErr:  fmt.Errorf("write door: EOF (%w)", beads.ErrMaybeApplied),
			ListResult: []beads.Bead{{ID: "obj-1.6", Title: "child", Status: "open", CreatedAt: time.Now().UTC().Format(time.RFC3339)}},
		}
		setupObjectiveProject(t, mock)
		stdout, stderr, read := withTempOutputFiles(t)
		code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "child", "--parent", "obj-1", "--waiting", "review:rev"}, stdout, stderr)
		out, errOut := read()
		if code != 0 || !strings.Contains(out, `"bead_id":"obj-1.6"`) {
			t.Fatalf("exit=%d out=%s err=%s", code, out, errOut)
		}
	})
}

// A create that the door lost track of, seen as a kill or a timeout rather than
// a hung-up connection, is looked up the same way.
func TestCreateKilledInTheDoorIsLookedUp(t *testing.T) {
	fastLandedLookup(t)
	now := time.Now().UTC().Format(time.RFC3339)
	for name, cerr := range map[string]error{
		"door flag":    fmt.Errorf("signal: killed (%w)", beads.ErrMaybeApplied),
		"older door":   errors.New("signal: killed"),
		"own deadline": context.DeadlineExceeded,
	} {
		t.Run(name, func(t *testing.T) {
			setupObjectiveProject(t, &beads.MockClient{CreateErr: cerr, ListResult: []beads.Bead{{ID: "obj-1.7", Title: "child", CreatedAt: now}}})
			stdout, stderr, read := withTempOutputFiles(t)
			code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "child", "--parent", "obj-1", "--waiting", "review:rev"}, stdout, stderr)
			out, errOut := read()
			if code != 0 || !strings.Contains(out, `"bead_id":"obj-1.7"`) {
				t.Fatalf("exit=%d out=%s err=%s", code, out, errOut)
			}
		})
	}
	t.Run("not found says to look first", func(t *testing.T) {
		setupObjectiveProject(t, &beads.MockClient{CreateErr: errors.New("signal: killed")})
		stdout, stderr, read := withTempOutputFiles(t)
		code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "child", "--parent", "obj-1", "--waiting", "review:rev"}, stdout, stderr)
		_, errOut := read()
		if code != 1 || !strings.Contains(errOut, "before creating it again") {
			t.Fatalf("exit=%d err=%s", code, errOut)
		}
	})
	t.Run("a refused create is not looked up", func(t *testing.T) {
		mock := &beads.MockClient{CreateErr: errors.New("no such parent"), ListResult: []beads.Bead{{ID: "obj-1.7", Title: "child", CreatedAt: now}}}
		setupObjectiveProject(t, mock)
		stdout, stderr, read := withTempOutputFiles(t)
		code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "child", "--parent", "obj-1", "--waiting", "review:rev"}, stdout, stderr)
		out, errOut := read()
		if code != 1 || strings.Contains(out, "obj-1.7") {
			t.Fatalf("exit=%d out=%s err=%s", code, out, errOut)
		}
	})
}

// listSeq answers List from one reply per look.
type listSeq struct {
	*beads.MockClient
	replies [][]beads.Bead
	errs    []error
	looks   int
}

func (l *listSeq) List(context.Context, string, beads.ListOptions) ([]beads.Bead, error) {
	i := min(l.looks, len(l.replies)-1)
	l.looks++
	return l.replies[i], l.errs[i]
}

// Each rejection, on its own: deleting a branch of the lookup fails here.
func TestFindLandedCreateRejects(t *testing.T) {
	fastLandedLookup(t)
	started := time.Now()
	fresh := started.UTC().Format(time.RFC3339)
	old := started.Add(-2 * time.Minute).UTC().Format(time.RFC3339)
	for _, c := range []struct {
		name string
		list []beads.Bead
		want string
	}{
		{"child by dotted id", []beads.Bead{{ID: "p.1", Title: "t", CreatedAt: fresh}}, "p.1"},
		{"child by parent field", []beads.Bead{{ID: "z-1", ParentID: "p", Title: "t", CreatedAt: fresh}}, "z-1"},
		{"other title", []beads.Bead{{ID: "p.1", Title: "u", CreatedAt: fresh}}, ""},
		{"other parent", []beads.Bead{{ID: "q.1", Title: "t", CreatedAt: fresh}}, ""},
		{"parent as a prefix of another id", []beads.Bead{{ID: "pp.1", Title: "t", CreatedAt: fresh}}, ""},
		{"too old", []beads.Bead{{ID: "p.1", Title: "t", CreatedAt: old}}, ""},
		{"unreadable time", []beads.Bead{{ID: "p.1", Title: "t", CreatedAt: "yesterday"}}, ""},
		{"two matches", []beads.Bead{{ID: "p.1", Title: "t", CreatedAt: fresh}, {ID: "p.2", Title: "t", CreatedAt: fresh}}, ""},
		{"none", nil, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := &beads.MockClient{ListResult: c.list}
			if got := findLandedCreate(m, "/d", "t", "p", "", started); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
	t.Run("marker", func(t *testing.T) {
		m := &beads.MockClient{ListResult: []beads.Bead{
			{ID: "p.1", Title: "t", CreatedAt: fresh},
			{ID: "p.2", Title: "t", Labels: []string{"other"}, CreatedAt: fresh},
			{ID: "p.3", Title: "t", Labels: []string{"mine"}, CreatedAt: fresh},
		}}
		if got := findLandedCreate(m, "/d", "t", "p", "mine", started); got != "p.3" {
			t.Fatalf("got %q", got)
		}
		if got := findLandedCreate(m, "/d", "t", "p", "gone", started); got != "" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("read failure", func(t *testing.T) {
		m := &beads.MockClient{ListErr: errors.New("busy"), ListResult: []beads.Bead{{ID: "p.1", Title: "t", CreatedAt: fresh}}}
		if got := findLandedCreate(m, "/d", "t", "p", "", started); got != "" {
			t.Fatalf("got %q", got)
		}
	})
}

// The door may still be writing when the answer is lost, and a look may fail.
func TestFindLandedCreateLooksAgain(t *testing.T) {
	fastLandedLookup(t)
	started := time.Now()
	found := []beads.Bead{{ID: "p.1", Title: "t", CreatedAt: started.UTC().Format(time.RFC3339)}}
	l := &listSeq{MockClient: &beads.MockClient{}, replies: [][]beads.Bead{nil, nil, found}, errs: []error{errors.New("busy"), nil, nil}}
	if got := findLandedCreate(l, "/d", "t", "p", "", started); got != "p.1" || l.looks != 3 {
		t.Fatalf("got %q after %d looks", got, l.looks)
	}
}

// The budget of the create can be gone when its answer is lost: links, the
// proposal and the mail still run.
func TestFollowUpsAfterALostAnswerHaveTheirOwnBudget(t *testing.T) {
	fastLandedLookup(t)
	old := beadWriteTimeout
	beadWriteTimeout = 200 * time.Millisecond
	t.Cleanup(func() { beadWriteTimeout = old })
	c := &expiringCreate{MockClient: &beads.MockClient{ListResult: []beads.Bead{{ID: "obj-1.8", Title: "child", CreatedAt: time.Now().UTC().Format(time.RFC3339)}}}}
	setupObjectiveProject(t, c)
	stdout, stderr, read := withTempOutputFiles(t)
	code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "child", "--parent", "obj-1", "--deps", "obj-1.2"}, stdout, stderr)
	out, errOut := read()
	if code != 0 || c.linkErr != nil || !c.linked {
		t.Fatalf("exit=%d link err=%v linked=%v out=%s err=%s", code, c.linkErr, c.linked, out, errOut)
	}
}

type expiringCreate struct {
	*beads.MockClient
	linked  bool
	linkErr error
}

func (c *expiringCreate) Create(ctx context.Context, _, _ string, _ beads.CreateOptions) (string, error) {
	<-ctx.Done()
	return "", fmt.Errorf("write door: %w (%w)", ctx.Err(), beads.ErrMaybeApplied)
}

func (c *expiringCreate) DependOn(ctx context.Context, _, _, _ string) error {
	c.linked, c.linkErr = true, ctx.Err()
	return c.linkErr
}

// A gate whose --before link will not write: exit 3 (so `&& spawn` stops), the
// bead's id, and a finish command that really works on a gate.
func TestUnlinkedGateStopsTheChainAndItsFinishCommandWorks(t *testing.T) {
	fastLinkRetries(t)
	m := &flakyLinks{MockClient: &beads.MockClient{CreateID: "obj-1.9", ShowResult: &beads.Bead{ID: "obj-1.9", Labels: []string{beads.GateLabel}}}, fails: 99}
	setupObjectiveProject(t, m)
	stdout, stderr, read := withTempOutputFiles(t)
	code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "Greenlight it", "--parent", "obj-1", "--gate", "--approver", "user", "--criteria", "The user says go", "--before", "obj-1.2", "--after", "obj-1.1"}, stdout, stderr)
	out, errOut := read()
	var res internalCreateObjectiveResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if code != exitUnlinked || res.BeadID != "obj-1.9" || res.Complete || len(res.Unlinked) != 2 {
		t.Fatalf("exit=%d out=%s err=%s", code, out, errOut)
	}
	if !strings.Contains(errOut, "do not create it again") {
		t.Fatalf("stderr: %s", errOut)
	}

	m.fails = 0
	for _, u := range res.Unlinked {
		words := strings.Fields(u.Finish)
		if len(words) < 3 || words[0] != "cmdr" || words[1] != "internal-gate" {
			t.Fatalf("finish = %q", u.Finish)
		}
		stdout, stderr, read := withTempOutputFiles(t)
		if code := runInternalGate(words[2:], stdout, stderr); code != 0 {
			_, e := read()
			t.Fatalf("%s: exit %d: %s", u.Finish, code, e)
		}
		if !slices.Contains(m.written, u.Waiter+"->"+u.On) {
			t.Fatalf("%s wrote %v, want %s->%s", u.Finish, m.written, u.Waiter, u.On)
		}
	}

	// The verb the finish command replaces refuses this edge.
	stdout, stderr, read = withTempOutputFiles(t)
	if code := runInternalDep([]string{"--project", "manifest", "add", "obj-1.2", "obj-1.9"}, stdout, stderr); code == 0 {
		t.Fatal("internal-dep takes a gate edge; the finish command need not differ")
	}
	read()
}

func TestUnlinkedRetriesInsideTheVerbFirst(t *testing.T) {
	fastLinkRetries(t)
	m := &flakyLinks{MockClient: &beads.MockClient{CreateID: "obj-1.9"}, fails: 2}
	setupObjectiveProject(t, m)
	stdout, stderr, read := withTempOutputFiles(t)
	code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "child", "--parent", "obj-1", "--deps", "obj-1.2"}, stdout, stderr)
	out, errOut := read()
	if code != 0 || !strings.Contains(out, `"complete":true`) || len(m.written) != 1 {
		t.Fatalf("exit=%d written=%v out=%s err=%s", code, m.written, out, errOut)
	}
}

// A link whose answer was lost is not written twice, and a refusal is final.
func TestWriteLinkLooksBeforeRepeatingAndStopsOnARefusal(t *testing.T) {
	fastLinkRetries(t)
	landed := &flakyLinks{MockClient: &beads.MockClient{DepsResult: map[string][]beads.Dep{"a": {{IssueID: "a", DependsOnID: "b", Type: "blocks"}}}}, fails: 99, err: fmt.Errorf("lost: %w", beads.ErrMaybeApplied)}
	if err := writeLink(context.Background(), landed, "/d", "a", "b"); err != nil || landed.tries != 1 {
		t.Fatalf("err=%v tries=%d", err, landed.tries)
	}
	refused := &flakyLinks{MockClient: &beads.MockClient{}, fails: 99, err: errors.New("no such issue")}
	if err := writeLink(context.Background(), refused, "/d", "a", "b"); err == nil || refused.tries != 1 {
		t.Fatalf("err=%v tries=%d", err, refused.tries)
	}
}

func TestGateLinkRefusals(t *testing.T) {
	cycle := &beads.MockClient{ShowResult: &beads.Bead{ID: "g"}, DepsResult: map[string][]beads.Dep{"x": {{IssueID: "x", DependsOnID: "g", Type: "blocks"}}}}
	for _, c := range []struct {
		name string
		mock *beads.MockClient
		args []string
		want string
	}{
		{"names no wait", &beads.MockClient{ShowResult: &beads.Bead{ID: "g"}}, nil, "--after"},
		{"no such bead", &beads.MockClient{}, []string{"--after", "x"}, "no bead g"},
		{"after a bead that waits on it", cycle, []string{"--after", "x"}, "cycle"},
		{"before a bead it waits on", &beads.MockClient{ShowResult: &beads.Bead{ID: "g"}, DepsResult: map[string][]beads.Dep{"g": {{IssueID: "g", DependsOnID: "x", Type: "blocks"}}}}, []string{"--before", "x"}, "cycle"},
		{"itself", &beads.MockClient{ShowResult: &beads.Bead{ID: "g"}}, []string{"--after", "g"}, "cycle"},
	} {
		t.Run(c.name, func(t *testing.T) {
			setupObjectiveProject(t, c.mock)
			stdout, stderr, read := withTempOutputFiles(t)
			code := runInternalGate(append([]string{"link", "--project", "manifest", "--bead", "g"}, c.args...), stdout, stderr)
			_, errOut := read()
			if code == 0 || !strings.Contains(errOut, c.want) {
				t.Fatalf("exit=%d err=%s", code, errOut)
			}
			if strings.Contains(strings.Join(c.mock.Calls, "\n"), "DependOn:") {
				t.Fatalf("wrote an edge: %v", c.mock.Calls)
			}
		})
	}
}

// Failures after the create reach the result, not only stderr.
func TestStartAndMailFailuresAreInWarnings(t *testing.T) {
	t.Run("Manager could not start", func(t *testing.T) {
		orig := newWorkstreamOpener
		newWorkstreamOpener = func() (workstreamOpener, func(), error) { return nil, nil, errors.New("no cmdrd") }
		t.Cleanup(func() { newWorkstreamOpener = orig })
		setupObjectiveProject(t, &beads.MockClient{CreateID: "obj-2"})
		stdout, stderr, read := withTempOutputFiles(t)
		code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "new work"}, stdout, stderr)
		out, errOut := read()
		var res internalCreateObjectiveResult
		_ = json.Unmarshal([]byte(strings.TrimSpace(out)), &res)
		if code != 0 || !res.Complete || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "its Manager could not start: no cmdrd") {
			t.Fatalf("exit=%d out=%s err=%s", code, out, errOut)
		}
	})
	t.Run("Commander could not be mailed", func(t *testing.T) {
		proposalFixture(t, true)
		orig := mailDeps
		mailDeps = func() (amAgentStore, amMailer, string, func(), error) {
			return nil, nil, "", nil, errors.New("no am token")
		}
		t.Cleanup(func() { mailDeps = orig })
		t.Setenv("COMMANDER_ROLE", "manager-ms-1")
		stdout, stderr, read := withTempOutputFiles(t)
		code := runInternalCreateObjective([]string{"--project", "manifest", "--parent", "ms-1", "--title", "Greenlight: pick the VMs", "--gate", "--approver", "user", "--criteria", "The user says go"}, stdout, stderr)
		out, errOut := read()
		var res internalCreateObjectiveResult
		_ = json.Unmarshal([]byte(strings.TrimSpace(out)), &res)
		if code != 0 || res.Proposed == "" || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "no am token") {
			t.Fatalf("exit=%d out=%s err=%s", code, out, errOut)
		}
	})
}

func fastLinkRetries(t *testing.T) {
	t.Helper()
	old := linkRetryWaits
	linkRetryWaits = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { linkRetryWaits = old })
}

func fastLandedLookup(t *testing.T) {
	t.Helper()
	old := landedLookupWaits
	landedLookupWaits = []time.Duration{0, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { landedLookupWaits = old })
}

// flakyLinks fails its first `fails` link writes.
type flakyLinks struct {
	*beads.MockClient
	fails, tries int
	err          error
	written      []string
}

func (f *flakyLinks) DependOn(ctx context.Context, dir, a, b string) error {
	f.tries++
	if f.tries <= f.fails {
		if f.err != nil {
			return f.err
		}
		return errors.New("br dep add: database is locked")
	}
	f.written = append(f.written, a+"->"+b)
	return nil
}

type failingDeps struct{ *beads.MockClient }

func (failingDeps) DependOn(context.Context, string, string, string) error { return fmt.Errorf("busy") }
