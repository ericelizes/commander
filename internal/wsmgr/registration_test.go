package wsmgr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/tui"

	"github.com/example-user/commander/internal/beads"
)

func TestRegistrationRejectsNestedAndNonObjectives(t *testing.T) {
	for _, tc := range []struct {
		id, parent, kind string
		allowed          bool
	}{
		{"demo-root.7", "", "kind:objective", true},
		{"demo-other", "demo-root.7", "kind:task", false},
		{"demo-gate", "demo-root.7", "kind:gate", false},
		{"demo-track", "demo-root.7", "kind:track", false},
		{"demo-loose-task", "", "kind:task", false},
		{"demo-loose-gate", "", "kind:gate", false},
		{"demo-gate-label", "", "gate", false}, // internal-create-objective --gate: kind:objective + gate
		{"demo-moved", "demo-root.7", "kind:objective", false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			bc := &beads.MemClient{EdgeParentsOnly: true}
			bc.Seed(beads.Bead{ID: "demo-root.7", Status: "open", Labels: []string{"kind:objective"}})
			labels := []string{tc.kind}
			if tc.kind == beads.GateLabel {
				labels = []string{"kind:objective", beads.GateLabel}
			}
			bc.Seed(beads.Bead{ID: tc.id, Status: "open", Labels: labels})
			if tc.parent != "" {
				bc.Edges = []beads.Dep{{IssueID: tc.id, DependsOnID: tc.parent, Type: "parent-child"}}
			}
			f.m.Beads = bc
			_, err := f.m.Prepare(ctx, Target{Project: f.proj, BeadID: tc.id})
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v err=%v", tc.allowed, err)
			}
			row, _ := f.st.GetWorkstreamManager(ctx, f.proj.ID, tc.id)
			if (row != nil) != tc.allowed {
				t.Fatalf("unexpected registration: %+v", row)
			}
			if !tc.allowed {
				if !errors.Is(err, ErrInvalidRoute) {
					t.Fatalf("invalid route not typed: %v", err)
				}
				if tc.parent != "" && !strings.Contains(err.Error(), Role(tc.parent)) {
					t.Fatalf("missing authoritative owner: %v", err)
				}
				if _, err := os.Stat(sessionFilePath(f.proj.Path, tc.id)); !os.IsNotExist(err) {
					t.Fatalf("session created: %v", err)
				}
			}
		})
	}
}

func TestRegistrationAncestryFailsClosed(t *testing.T) {
	f := newFixture(t)
	f.bc.ParentsErr = errors.New("unavailable")
	if _, err := f.m.Prepare(context.Background(), Target{Project: f.proj, BeadID: "example-app-t11"}); err == nil {
		t.Fatal("registered on failed ancestry")
	}
}

func TestStrictRootMissingAndCyclicAncestry(t *testing.T) {
	ctx := context.Background()
	for _, bc := range []beads.Client{
		&beads.MemClient{EdgeParentsOnly: true},
		&beads.MockClient{ParentsResult: map[string]string{"demo-a": "demo-b", "demo-b": "demo-a"}},
	} {
		if root, err := StrictRoot(ctx, bc, "/fixture", "demo-a"); err == nil {
			t.Fatalf("root=%s without error", root)
		}
	}
}

func TestOpenDoesNotTrustStaleNestedDisplayRoot(t *testing.T) {
	f := newFixture(t)
	f.bc.ParentsResult = map[string]string{"example-app-t11": "example-app-t01", "example-app-t01": ""}
	result, err := f.m.Open(context.Background(), f.proj.Name, "example-app-t11", OpenOptions{})
	if err != nil || result.BeadID != "example-app-t01" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	row, _ := f.st.GetWorkstreamManager(context.Background(), f.proj.ID, "example-app-t11")
	if row != nil {
		t.Fatal("stale display registered nested Manager")
	}
}

// busyOn refuses fresh reads of one bead, as br does under load.
type busyOn struct {
	beads.Client
	id string
}

func (b busyOn) Details(ctx context.Context, dir string, ids []string) (map[string]beads.Detail, error) {
	for _, id := range ids {
		if id == b.id {
			return nil, beads.ErrBusy
		}
	}
	return b.Client.Details(ctx, dir, ids)
}

// mixedRegistration: example-app-t11 is a real root; example-app-t11.4 is a
// nested objective the display promoted (no parent row); example-app-t01 is a
// real root whose ancestry read is refused.
func mixedRegistration(f *fixture) {
	bc := &beads.MemClient{EdgeParentsOnly: true}
	for _, id := range []string{"example-app-t11", "example-app-t11.4", "example-app-t01"} {
		bc.Seed(beads.Bead{ID: id, Title: id, Status: "open", Labels: []string{"kind:objective"}})
	}
	bc.Edges = []beads.Dep{{IssueID: "example-app-t11.4", DependsOnID: "example-app-t11", Type: "parent-child"}}
	f.m.Beads = busyOn{Client: bc, id: "example-app-t01"}
	f.m.Numbered = func(context.Context) ([]tui.NumberedWorkstream, error) {
		return []tui.NumberedWorkstream{
			{N: 1, Project: "example-app", BeadID: "example-app-t11.4", Title: "Nested step"},
			{N: 2, Project: "example-app", BeadID: "example-app-t01", Title: "Busy root"},
			{N: 3, Project: "example-app", BeadID: "example-app-t11", Title: "Real root"},
		}, nil
	}
}

// noSideEffects fails when anything was written for a refused Manager.
func noSideEffects(t *testing.T, f *fixture, id string) {
	t.Helper()
	ctx := context.Background()
	role := Role(id)
	if row, _ := f.st.GetWorkstreamManager(ctx, f.proj.ID, id); row != nil {
		t.Errorf("%s registered: %+v", id, row)
	}
	if name, _ := f.st.GetAmAgent(ctx, f.m.AmKey, role); name != "" {
		t.Errorf("%s got a mail agent %q", role, name)
	}
	for _, path := range []string{sessionFilePath(f.proj.Path, id), agentrt.ClaudePromptPath(role), chat.RuntimeFile(os.Getenv("COMMANDER_HOME"), role)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s wrote %s (%v)", role, path, err)
		}
	}
}

func registerCalls(t *testing.T, f *fixture, want int) {
	t.Helper()
	if len(f.reg.RegisterCalls) != want {
		t.Errorf("am registrations = %d, want %d: %+v", len(f.reg.RegisterCalls), want, f.reg.RegisterCalls)
	}
}

func TestPrepareAllSkipsInvalidTargetsAndKeepsValidWindows(t *testing.T) {
	f := newFixture(t)
	mixedRegistration(f)
	ws, err := f.m.PrepareAll(context.Background())
	if len(ws) != 1 || ws[0].BeadID != "example-app-t11" || ws[0].Rank != 3 {
		t.Fatalf("windows = %+v", ws)
	}
	if err == nil || !strings.Contains(err.Error(), "skipped example-app-t11.4") || !strings.Contains(err.Error(), "skipped example-app-t01") || !beads.IsBusy(err) {
		t.Fatalf("err = %v", err)
	}
	for _, id := range []string{"example-app-t11.4", "example-app-t01"} {
		noSideEffects(t, f, id)
	}
	registerCalls(t, f, 1) // the real root only
	if row, _ := f.st.GetWorkstreamManager(context.Background(), f.proj.ID, "example-app-t11"); row == nil {
		t.Fatal("the real root lost its Manager")
	}
}

func TestEnsureNativeValidatesBeforeAnySideEffect(t *testing.T) {
	f := newFixture(t)
	mixedRegistration(f)
	old := codexHooksReady
	codexHooksReady = func() bool { return true }
	t.Cleanup(func() { codexHooksReady = old })
	t.Setenv("COMMANDER_CODEX_MANAGER_SHARE", "1") // a draw would write a runtime file
	n := &fakeNative{}
	f.m.Native = n
	err := f.m.EnsureNative(context.Background())
	if err == nil || !strings.Contains(err.Error(), "nested under example-app-t11") {
		t.Fatalf("err = %v", err)
	}
	if !n.started["manager-example-app-t11"] || n.started["manager-example-app-t11.4"] || n.started["manager-example-app-t01"] {
		t.Fatalf("started = %v", n.started)
	}
	for _, id := range []string{"example-app-t11.4", "example-app-t01"} {
		noSideEffects(t, f, id)
	}
	registerCalls(t, f, 1)
}

func TestAdoptValidatesBeforeAnySideEffect(t *testing.T) {
	for _, id := range []string{"example-app-t11.4", "example-app-t01"} {
		t.Run(id, func(t *testing.T) {
			f := newFixture(t)
			mixedRegistration(f)
			ctx := context.Background()
			orig := filepath.Join(f.proj.Path, ".commander", "manager.claude.session-id")
			if err := os.MkdirAll(filepath.Dir(orig), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(orig, []byte("33333333-3333-4333-8333-333333333333\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := f.st.UpsertAmAgent(ctx, "/repo", "manager-example-app", "GoldTiger"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.m.Adopt(ctx, "example-app", id, "manager (example-app)"); err == nil {
				t.Fatal("adopted an invalid workstream")
			}
			noSideEffects(t, f, id)
			registerCalls(t, f, 0)
			if name, _ := f.st.GetAmAgent(ctx, "/repo", "manager-example-app"); name != "GoldTiger" {
				t.Fatalf("old mail role moved: %q", name)
			}
			if calls := f.mr.CallStrings(); len(calls) != 0 {
				t.Fatalf("window touched:\n%s", strings.Join(calls, "\n"))
			}
		})
	}
}
