package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/wsmgr"
)

func placeFixture(t *testing.T) (*beads.MemClient, func(title, parent string, labels ...string) string) {
	t.Helper()
	m := &beads.MemClient{Prefix: "p", EdgeParentsOnly: true}
	return m, func(title, parent string, labels ...string) string {
		id, err := m.Create(context.Background(), "", title, beads.CreateOptions{Parent: parent, Labels: append([]string{"kind:objective"}, labels...)})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
}

func parentOfBead(t *testing.T, m *beads.MemClient, id string) string {
	t.Helper()
	d, err := m.Details(context.Background(), "", []string{id})
	if err != nil {
		t.Fatal(err)
	}
	return d[id].Parent
}

func TestPlaceBead_NestsAtAnyDepth(t *testing.T) {
	ctx := context.Background()
	m, add := placeFixture(t)
	a := add("a", "", beads.WorkstreamLabel)
	b := add("b", "", beads.WorkstreamLabel)
	c := add("c", a, beads.WorkstreamLabel)
	if moved, _, err := placeBead(ctx, m, "", b, c); err != nil || !moved {
		t.Fatalf("moved=%v err=%v", moved, err)
	}
	if parentOfBead(t, m, b) != c {
		t.Fatalf("parent %q", parentOfBead(t, m, b))
	}
	if moved, _, err := placeBead(ctx, m, "", b, c); err != nil || moved {
		t.Fatalf("the same place again: moved=%v err=%v", moved, err)
	}
}

// A drop on a workstream with open steps never breaks the shape: they move aside.
func TestPlaceBead_OpenStepsMoveIntoTheirOwnWorkstream(t *testing.T) {
	ctx := context.Background()
	m, add := placeFixture(t)
	leaf := add("Leaf title", "", beads.WorkstreamLabel)
	step := add("a step", leaf)
	other := add("other", "", beads.WorkstreamLabel)
	_, wrapped, err := placeBead(ctx, m, "", other, leaf)
	if err != nil {
		t.Fatal(err)
	}
	if wrapped == "" || parentOfBead(t, m, step) != wrapped || parentOfBead(t, m, wrapped) != leaf || parentOfBead(t, m, other) != leaf {
		t.Fatalf("wrapped=%q step under %q, wrapper under %q, other under %q", wrapped, parentOfBead(t, m, step), parentOfBead(t, m, wrapped), parentOfBead(t, m, other))
	}
	w, _ := m.Show(ctx, "", wrapped)
	if w.Title != "Leaf title" || !slices.Contains(w.Labels, beads.WorkstreamLabel) {
		t.Fatalf("wrapper %+v", w)
	}
	if err := beads.CheckPlacement(ctx, m, "", leaf, beads.Placement{Workstream: true}); err != nil {
		t.Fatalf("the shape broke: %v", err)
	}
}

func TestPlaceBead_ToTheTop(t *testing.T) {
	ctx := context.Background()
	m, add := placeFixture(t)
	a := add("a", "", beads.WorkstreamLabel)
	b := add("b", a, beads.TrackLabel)
	if _, _, err := placeBead(ctx, m, "", b, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := m.Show(ctx, "", b)
	if parentOfBead(t, m, b) != "" || !slices.Contains(got.Labels, beads.WorkstreamLabel) || slices.Contains(got.Labels, beads.TrackLabel) {
		t.Fatalf("parent %q labels %v", parentOfBead(t, m, b), got.Labels)
	}
}

func TestPlaceBead_Refuses(t *testing.T) {
	ctx := context.Background()
	m, add := placeFixture(t)
	a := add("a", "", beads.WorkstreamLabel)
	b := add("b", a, beads.WorkstreamLabel)
	step := add("step", b)
	if _, _, err := placeBead(ctx, m, "", a, b); err == nil || !strings.Contains(err.Error(), "descendant") {
		t.Fatalf("under its own child: %v", err)
	}
	c := add("c", "", beads.WorkstreamLabel)
	if _, _, err := placeBead(ctx, m, "", c, step); err == nil || !strings.Contains(err.Error(), "is a step") {
		t.Fatalf("under a step: %v", err)
	}
	if parentOfBead(t, m, c) != "" {
		t.Fatal("a refused move still moved")
	}
}

// A workstream moved under another gives up its registration, and mail cannot start
// a Manager with none. Told while stopped, the notice sits unread and health says
// the role cannot be reached, for good. Its new owner hears either way.
func TestSettleOwnership_OldManagerHearsOnlyWhileItCanListen(t *testing.T) {
	for _, tc := range []struct {
		name     string
		answer   string // cmdrd's status of the old role, %s its name; "" = cmdrd is down
		wantTold bool
	}{
		{name: "stopped", answer: `{"ok":true,"status":{"role":"%s"}}`},
		{name: "running", answer: `{"ok":true,"status":{"role":"%s","running":true}}`, wantTold: true},
		{name: "held by a terminal", answer: `{"ok":true,"status":{"role":"%s","owned_by":"terminal"}}`, wantTold: true},
		{name: "cmdrd could not read the owner", answer: `{"ok":true,"status":{"role":"%s","owner_unknown":true}}`, wantTold: true},
		{name: "cmdrd cannot say", wantTold: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			home := t.TempDir()
			t.Setenv("COMMANDER_HOME", home)
			t.Setenv("COMMANDER_STATE_DB", "")
			var asked string
			if tc.answer != "" {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					asked = r.Method + " " + r.URL.Path
					w.Write([]byte(strings.ReplaceAll(tc.answer, "%s", strings.TrimPrefix(r.URL.Path, "/api/chat/"))))
				}))
				defer srv.Close()
				if err := os.WriteFile(filepath.Join(home, "api-url"), []byte(srv.URL), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			st, cleanup, err := needsYouStoreFn()
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			id, err := st.InsertProject(ctx, store.Project{Name: "demo", Type: "code", Path: home, Status: "active", ActivatedAt: 1})
			if err != nil {
				t.Fatal(err)
			}
			m, add := placeFixture(t)
			root := add("root", "", beads.WorkstreamLabel)
			moved := add("moved", "", beads.WorkstreamLabel)
			if err := st.UpsertWorkstreamManager(ctx, store.WorkstreamManager{ProjectID: id, WorkstreamBeadID: moved, AmRole: wsmgr.Role(moved), SessionFile: filepath.Join(home, "s")}); err != nil {
				t.Fatal(err)
			}
			if err := m.SetParent(ctx, "", moved, root); err != nil {
				t.Fatal(err)
			}
			am := newFakeAmStore()
			am.m[amKey+"\x00"+wsmgr.Role(moved)] = "OldManager"
			am.m[amKey+"\x00"+wsmgr.Role(root)] = "NewManager"
			am.m[amKey+"\x00commander"] = "TheCommander"
			mailer := &fakeMailer{}
			orig := mailDeps
			mailDeps = func() (amAgentStore, amMailer, string, func(), error) { return am, mailer, amKey, func() {}, nil }
			t.Cleanup(func() { mailDeps = orig })

			var warnings []string
			settleOwnership(ctx, m, &store.Project{ID: id, Name: "demo", Path: home}, moved, moved, func(s string) { warnings = append(warnings, s) })

			if len(warnings) != 0 {
				t.Fatalf("warnings: %v", warnings)
			}
			var told []string
			for _, s := range mailer.sent {
				told = append(told, s.to...)
			}
			if got := slices.Contains(told, "OldManager"); got != tc.wantTold {
				t.Fatalf("old Manager told=%v, want %v (mail to %v)", got, tc.wantTold, told)
			}
			if !slices.Contains(told, "NewManager") {
				t.Fatalf("the new owner must always hear (mail to %v)", told)
			}
			if row, err := st.GetWorkstreamManagerByRole(ctx, wsmgr.Role(moved)); err != nil || row != nil {
				t.Fatalf("the moved row stays registered: %+v, %v", row, err)
			}
			if want := "GET /api/chat/" + wsmgr.Role(moved); tc.answer != "" && asked != want {
				t.Fatalf("asked cmdrd %q, want %q", asked, want)
			}
		})
	}
}

// A step moving between two workstreams leaves a registered Manager behind: that one
// is told as ever, with no question to cmdrd.
func TestSettleOwnership_NestedMoveTellsBothManagers(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_STATE_DB", "")
	m, add := placeFixture(t)
	from := add("from", "", beads.WorkstreamLabel)
	into := add("into", "", beads.WorkstreamLabel)
	moved := add("moved", into, beads.WorkstreamLabel)
	am := newFakeAmStore()
	am.m[amKey+"\x00"+wsmgr.Role(from)] = "OldManager"
	am.m[amKey+"\x00"+wsmgr.Role(into)] = "NewManager"
	am.m[amKey+"\x00commander"] = "TheCommander"
	mailer := &fakeMailer{}
	orig := mailDeps
	mailDeps = func() (amAgentStore, amMailer, string, func(), error) { return am, mailer, amKey, func() {}, nil }
	t.Cleanup(func() { mailDeps = orig })
	var asks atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { asks.Add(1) }))
	defer srv.Close()
	writeTestFile(t, filepath.Join(home, "api-url"), srv.URL)

	settleOwnership(ctx, m, &store.Project{ID: 1, Name: "demo", Path: home}, moved, from, func(s string) { t.Errorf("warning: %s", s) })

	if n := asks.Load(); n != 0 {
		t.Fatalf("asked cmdrd %d times about a Manager that stays registered", n)
	}

	var told []string
	for _, s := range mailer.sent {
		told = append(told, s.to...)
	}
	if !slices.Contains(told, "OldManager") || !slices.Contains(told, "NewManager") {
		t.Fatalf("mail to %v", told)
	}
}

// The real Engine and StoreResolver behind cmdrd's answer shape, with the owner cache
// cold: only the row's deletion stands between a read and the session it names.
func TestSettleOwnership_RealEngineReadsTheOldManagerBeforeItsRowGoes(t *testing.T) {
	const session = "11111111-1111-4111-8111-111111111111"
	for _, tc := range []struct {
		name        string
		sessionFile func(t *testing.T, path string)
		noRow       bool
		procs       chat.ProcScanner
		wantTold    bool
	}{
		{
			name:        "never registered",
			sessionFile: func(*testing.T, string) {},
			noRow:       true,
			procs:       func(context.Context, string) ([]chat.Proc, error) { return nil, nil },
		},
		{
			name:        "held by a terminal",
			sessionFile: func(t *testing.T, path string) { writeTestFile(t, path, session+"\n") },
			procs: func(_ context.Context, id string) ([]chat.Proc, error) {
				return []chat.Proc{{PID: 4242, Command: "claude --resume " + id}}, nil
			},
			wantTold: true,
		},
		{
			name:        "stopped",
			sessionFile: func(t *testing.T, path string) { writeTestFile(t, path, session+"\n") },
			procs:       func(context.Context, string) ([]chat.Proc, error) { return nil, nil },
		},
		{
			name:        "process scan failed",
			sessionFile: func(t *testing.T, path string) { writeTestFile(t, path, session+"\n") },
			procs:       func(context.Context, string) ([]chat.Proc, error) { return nil, errors.New("ps: cannot fork") },
			wantTold:    true,
		},
		{
			name: "session file unreadable",
			sessionFile: func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0o700); err != nil { // reading a directory fails, as any unreadable file does
					t.Fatal(err)
				}
			},
			procs:    func(context.Context, string) ([]chat.Proc, error) { return nil, nil },
			wantTold: true,
		},
		{
			name:        "never had a session",
			sessionFile: func(*testing.T, string) {},
			procs:       func(context.Context, string) ([]chat.Proc, error) { return nil, nil },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("COMMANDER_HOME", home)
			t.Setenv("COMMANDER_STATE_DB", "")
			st, cleanup, err := needsYouStoreFn()
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			id, err := st.InsertProject(ctx, store.Project{Name: "demo", Type: "code", Path: home, Status: "active", ActivatedAt: 1})
			if err != nil {
				t.Fatal(err)
			}
			m, add := placeFixture(t)
			root := add("root", "", beads.WorkstreamLabel)
			moved := add("moved", "", beads.WorkstreamLabel)
			role := wsmgr.Role(moved)
			sessionFile := filepath.Join(home, "s.session-id")
			tc.sessionFile(t, sessionFile)
			if !tc.noRow {
				if err := st.UpsertWorkstreamManager(ctx, store.WorkstreamManager{ProjectID: id, WorkstreamBeadID: moved, AmRole: role, SessionFile: sessionFile}); err != nil {
					t.Fatal(err)
				}
			}
			if err := m.SetParent(ctx, "", moved, root); err != nil {
				t.Fatal(err)
			}
			e := chat.New(chat.Options{Home: home, Resolver: chat.StoreResolver{Store: st, Home: home}, Procs: tc.procs})
			t.Cleanup(e.Close)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				s, err := e.Status(role)
				if err != nil {
					t.Error(err)
				}
				json.NewEncoder(w).Encode(map[string]any{"ok": true, "status": s})
			}))
			defer srv.Close()
			writeTestFile(t, filepath.Join(home, "api-url"), srv.URL)
			told := placeMailers(t, moved, root)

			settleOwnership(ctx, m, &store.Project{ID: id, Name: "demo", Path: home}, moved, moved, func(s string) { t.Errorf("warning: %s", s) })

			if got := slices.Contains(told(), "OldManager"); got != tc.wantTold {
				t.Fatalf("old Manager told=%v, want %v (mail to %v)", got, tc.wantTold, told())
			}
			if !slices.Contains(told(), "NewManager") {
				t.Fatalf("the new owner must always hear (mail to %v)", told())
			}
		})
	}
}

// A 200 that says nothing about the role is no answer: it must not read as stopped.
func TestSettleOwnership_AnAnswerThatNamesNoChatCannotSay(t *testing.T) {
	for _, body := range []string{"<html>proxy</html>", "", "{}", `{"ok":true,"status":{}}`, `{"ok":true,"status":{"role":"manager-another"}}`} {
		t.Run(body, func(t *testing.T) {
			ctx := context.Background()
			home := t.TempDir()
			t.Setenv("COMMANDER_HOME", home)
			t.Setenv("COMMANDER_STATE_DB", "")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			defer srv.Close()
			writeTestFile(t, filepath.Join(home, "api-url"), srv.URL)
			m, add := placeFixture(t)
			root := add("root", "", beads.WorkstreamLabel)
			moved := add("moved", root, beads.WorkstreamLabel)
			told := placeMailers(t, moved, root)

			settleOwnership(ctx, m, &store.Project{ID: 1, Name: "demo", Path: home}, moved, moved, func(string) {})

			if !slices.Contains(told(), "OldManager") {
				t.Fatalf("read as stopped (mail to %v)", told())
			}
		})
	}
}

// A cmdrd that accepts and never answers costs its own short read, not the context
// both notices are sent on.
func TestSettleOwnership_AHungStatusReadLeavesTimeForTheNotices(t *testing.T) {
	orig := formerManagerReadTimeout
	formerManagerReadTimeout = 50 * time.Millisecond
	t.Cleanup(func() { formerManagerReadTimeout = orig })
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_STATE_DB", "")
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-done }))
	defer srv.Close()
	defer close(done)
	writeTestFile(t, filepath.Join(home, "api-url"), srv.URL)
	m, add := placeFixture(t)
	root := add("root", "", beads.WorkstreamLabel)
	moved := add("moved", root, beads.WorkstreamLabel)
	told := placeMailers(t, moved, root)
	start := time.Now()

	settleOwnership(ctx, m, &store.Project{ID: 1, Name: "demo", Path: home}, moved, moved, func(s string) { t.Errorf("warning: %s", s) })

	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("the read held the move for %v", elapsed)
	}
	if got := told(); !slices.Contains(got, "OldManager") || !slices.Contains(got, "NewManager") {
		t.Fatalf("mail to %v", got)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// placeMailers fakes mail for a move of moved under root and reads back who was told.
func placeMailers(t *testing.T, moved, root string) func() []string {
	t.Helper()
	am := newFakeAmStore()
	am.m[amKey+"\x00"+wsmgr.Role(moved)] = "OldManager"
	am.m[amKey+"\x00"+wsmgr.Role(root)] = "NewManager"
	am.m[amKey+"\x00commander"] = "TheCommander"
	mailer := &fakeMailer{}
	orig := mailDeps
	mailDeps = func() (amAgentStore, amMailer, string, func(), error) { return am, mailer, amKey, func() {}, nil }
	t.Cleanup(func() { mailDeps = orig })
	return func() []string {
		var told []string
		for _, s := range mailer.sent {
			told = append(told, s.to...)
		}
		return told
	}
}

// Moving the row back out leaves the parent as it was.
func TestPlaceBead_MovingAwayFoldsTheStepsBackIn(t *testing.T) {
	ctx := context.Background()
	m, add := placeFixture(t)
	leaf := add("Leaf title", "", beads.WorkstreamLabel)
	step := add("a step", leaf)
	other := add("other", "", beads.WorkstreamLabel)
	_, wrapped, err := placeBead(ctx, m, "", other, leaf)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := placeBead(ctx, m, "", other, ""); err != nil {
		t.Fatal(err)
	}
	if parentOfBead(t, m, step) != leaf {
		t.Fatalf("step under %q", parentOfBead(t, m, step))
	}
	if w, _ := m.Show(ctx, "", wrapped); w.Status != "closed" {
		t.Fatalf("holder %s", w.Status)
	}
}

// A real child of the same parent is left alone.
func TestPlaceBead_MovingAwayKeepsARealChild(t *testing.T) {
	ctx := context.Background()
	m, add := placeFixture(t)
	parent := add("Parent", "", beads.WorkstreamLabel)
	real := add("A real outcome", parent, beads.WorkstreamLabel)
	other := add("other", parent, beads.WorkstreamLabel)
	if _, _, err := placeBead(ctx, m, "", other, ""); err != nil {
		t.Fatal(err)
	}
	if w, _ := m.Show(ctx, "", real); w.Status == "closed" || parentOfBead(t, m, real) != parent {
		t.Fatalf("real child %+v under %q", w, parentOfBead(t, m, real))
	}
}

// With nested Managers on, a step moved between nested siblings changes Manager:
// both nested Managers hear, the root is not involved.
func TestSettleOwnership_MovedBetweenNestedSiblings(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_STATE_DB", "")
	if err := beads.SetNestedManagers(home, true); err != nil {
		t.Fatal(err)
	}
	m, add := placeFixture(t)
	root := add("root", "", beads.WorkstreamLabel)
	a := add("a", root, beads.WorkstreamLabel)
	b := add("b", root, beads.WorkstreamLabel)
	step := add("step", b)
	am := newFakeAmStore()
	am.m[amKey+"\x00"+wsmgr.Role(a)] = "ManagerA"
	am.m[amKey+"\x00"+wsmgr.Role(b)] = "ManagerB"
	am.m[amKey+"\x00"+wsmgr.Role(root)] = "RootManager"
	am.m[amKey+"\x00commander"] = "TheCommander"
	mailer := &fakeMailer{}
	orig := mailDeps
	mailDeps = func() (amAgentStore, amMailer, string, func(), error) { return am, mailer, amKey, func() {}, nil }
	t.Cleanup(func() { mailDeps = orig })

	settleOwnership(ctx, m, &store.Project{ID: 1, Name: "demo", Path: home}, step, a, func(s string) { t.Errorf("warning: %s", s) })

	var told []string
	for _, s := range mailer.sent {
		told = append(told, s.to...)
	}
	if !slices.Contains(told, "ManagerA") || !slices.Contains(told, "ManagerB") || slices.Contains(told, "RootManager") {
		t.Fatalf("mail to %v, want both nested Managers and not the root", told)
	}
}
