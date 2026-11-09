package handoff

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
)

// fakeStore captures InsertObjectiveTask + CountRunsByProject calls.
type fakeStore struct {
	inserts      []store.ObjectiveTask
	insertErr    error
	runningCount int
	countErr     error
}

func (f *fakeStore) InsertObjectiveTask(_ context.Context, t store.ObjectiveTask) (bool, error) {
	if f.insertErr != nil {
		return false, f.insertErr
	}
	f.inserts = append(f.inserts, t)
	return true, nil
}

func (f *fakeStore) CountRunsByProject(_ context.Context, _ int64, _ string) (int, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}
	return f.runningCount, nil
}

// fakeSpawner records each Spawn invocation. errOn returns an error on the
// nth (0-based) Spawn call when set; otherwise a synthetic success result.
type fakeSpawner struct {
	calls []spawn.SpawnRequest
	errOn int // -1 = never error
	err   error
}

func newFakeSpawner() *fakeSpawner { return &fakeSpawner{errOn: -1} }

func (f *fakeSpawner) Spawn(_ context.Context, req spawn.SpawnRequest) (*spawn.SpawnResult, error) {
	idx := len(f.calls)
	f.calls = append(f.calls, req)
	if f.errOn == idx {
		return nil, f.err
	}
	return &spawn.SpawnResult{
		RunID:       int64(idx + 1),
		AgentName:   "operator-" + req.Branch,
		ContainerID: fmt.Sprintf("ctr-%d", idx),
	}, nil
}

// writeOutput writes a v1 artifact with N titled entries to a temp file.
func writeOutput(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	var entries strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			entries.WriteString(",")
		}
		fmt.Fprintf(&entries, `{"title":"Entry %d","description":"desc %d","context":{"i":%d}}`, i, i, i)
	}
	body := fmt.Sprintf(`{"schema":"task-output@v1","entries":[%s]}`, entries.String())
	p := filepath.Join(dir, "output.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func TestSpawnFromOutput_CapacityPartition(t *testing.T) {
	// 5 entries, cap=3, no operators currently running → 3 spawn, 2 queue.
	path := writeOutput(t, 5)

	mock := &beads.MockClient{CreateID: "bd-derived"}
	st := &fakeStore{runningCount: 0}
	sp := newFakeSpawner()

	d := NewDerivedSpawner(mock, sp, st, Limits{MaxOperatorsPerProject: 3})
	res, err := d.SpawnFromOutput(context.Background(), SpawnFromOutputRequest{
		OutputPath:   path,
		ParentTaskID: "bd-parent",
		ProjectID:    42,
		ProjectName:  "demo",
		ProjectPath:  "/tmp/demo",
		ProjectType:  "code",
		ObjectiveID:  7,
		Model:        "anthropic/claude-sonnet-4-5",
	})
	if err != nil {
		t.Fatalf("SpawnFromOutput: %v", err)
	}

	if got := len(res.Spawned); got != 3 {
		t.Errorf("Spawned = %d; want 3", got)
	}
	if got := len(res.Queued); got != 2 {
		t.Errorf("Queued = %d; want 2", got)
	}

	// All 5 derived beads created.
	createCalls := 0
	for _, c := range mock.Calls {
		if strings.HasPrefix(c, "Create:") {
			createCalls++
			if !strings.Contains(c, "parent=bd-parent") {
				t.Errorf("create call missing parent: %s", c)
			}
			if !strings.Contains(c, "kind:task,kind:derived") {
				t.Errorf("create call missing labels: %s", c)
			}
			if !strings.Contains(c, "silent=true") {
				t.Errorf("create call missing silent: %s", c)
			}
		}
	}
	if createCalls != 5 {
		t.Errorf("bd.Create calls = %d; want 5", createCalls)
	}

	// All 5 objective_task rows inserted with is_derived + parent ref.
	if got := len(st.inserts); got != 5 {
		t.Errorf("InsertObjectiveTask calls = %d; want 5", got)
	}
	for i, ot := range st.inserts {
		if !ot.IsDerived {
			t.Errorf("inserts[%d].IsDerived = false; want true", i)
		}
		if !ot.DerivedFromTaskBeadID.Valid || ot.DerivedFromTaskBeadID.String != "bd-parent" {
			t.Errorf("inserts[%d].DerivedFromTaskBeadID = %v; want bd-parent", i, ot.DerivedFromTaskBeadID)
		}
		if ot.ProjectID != 42 {
			t.Errorf("inserts[%d].ProjectID = %d; want 42", i, ot.ProjectID)
		}
		if ot.ObjectiveID != 7 {
			t.Errorf("inserts[%d].ObjectiveID = %d; want 7", i, ot.ObjectiveID)
		}
	}

	// First 3 spawned via OperatorSpawner; remaining 2 are queued.
	if got := len(sp.calls); got != 3 {
		t.Errorf("Spawn calls = %d; want 3", got)
	}
	for i, c := range sp.calls {
		if c.ProjectType != "code" {
			t.Errorf("Spawn[%d].ProjectType = %q; want code", i, c.ProjectType)
		}
		if !strings.HasPrefix(c.Branch, "derived/") {
			t.Errorf("Spawn[%d].Branch = %q; want derived/* prefix", i, c.Branch)
		}
		if c.TaskBeadID != "bd-derived" {
			t.Errorf("Spawn[%d].TaskBeadID = %q", i, c.TaskBeadID)
		}
	}

	// Queued entries carry parameterized prompts containing the upstream
	// description.
	for i, q := range res.Queued {
		if !strings.Contains(q.Prompt, "Description:") {
			t.Errorf("Queued[%d].Prompt missing Description: %q", i, q.Prompt)
		}
		if !strings.Contains(q.Prompt, "Context:") {
			t.Errorf("Queued[%d].Prompt missing Context: %q", i, q.Prompt)
		}
	}
}

func TestSpawnFromOutput_AlreadyAtCapacity(t *testing.T) {
	// 4 entries, cap=3, 3 already running → 0 spawn, 4 queue.
	path := writeOutput(t, 4)

	d := NewDerivedSpawner(
		&beads.MockClient{CreateID: "bd-d"},
		newFakeSpawner(),
		&fakeStore{runningCount: 3},
		Limits{MaxOperatorsPerProject: 3},
	)
	res, err := d.SpawnFromOutput(context.Background(), SpawnFromOutputRequest{
		OutputPath:   path,
		ParentTaskID: "bd-parent",
		ProjectID:    1,
		ProjectType:  "code",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(res.Spawned) != 0 {
		t.Errorf("Spawned = %d; want 0", len(res.Spawned))
	}
	if len(res.Queued) != 4 {
		t.Errorf("Queued = %d; want 4", len(res.Queued))
	}
}

func TestSpawnFromOutput_NoCap(t *testing.T) {
	// cap=0 means unlimited.
	path := writeOutput(t, 6)
	sp := newFakeSpawner()
	d := NewDerivedSpawner(
		&beads.MockClient{CreateID: "bd-d"},
		sp,
		&fakeStore{},
		Limits{MaxOperatorsPerProject: 0},
	)
	res, err := d.SpawnFromOutput(context.Background(), SpawnFromOutputRequest{
		OutputPath:  path,
		ProjectType: "code",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(res.Spawned) != 6 {
		t.Errorf("Spawned = %d; want 6", len(res.Spawned))
	}
	if len(res.Queued) != 0 {
		t.Errorf("Queued = %d; want 0", len(res.Queued))
	}
	if len(sp.calls) != 6 {
		t.Errorf("Spawn calls = %d; want 6", len(sp.calls))
	}
}

func TestSpawnFromOutput_KnowledgeProjectNoBranch(t *testing.T) {
	path := writeOutput(t, 2)
	sp := newFakeSpawner()
	d := NewDerivedSpawner(
		&beads.MockClient{CreateID: "bd-d"},
		sp,
		&fakeStore{},
		Limits{},
	)
	_, err := d.SpawnFromOutput(context.Background(), SpawnFromOutputRequest{
		OutputPath:  path,
		ProjectType: "knowledge",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	for i, c := range sp.calls {
		if c.Branch != "" {
			t.Errorf("Spawn[%d].Branch = %q; want empty for knowledge", i, c.Branch)
		}
	}
}

func TestSpawnFromOutput_BeadCreateFailureStopsLoop(t *testing.T) {
	// First Create succeeds (mock returns same id every time), but make
	// the Create call error to verify we surface the error mid-loop.
	path := writeOutput(t, 3)
	mock := &beads.MockClient{CreateID: "bd-d", CreateErr: errors.New("bd boom")}
	d := NewDerivedSpawner(mock, newFakeSpawner(), &fakeStore{}, Limits{})
	res, err := d.SpawnFromOutput(context.Background(), SpawnFromOutputRequest{
		OutputPath:  path,
		ProjectType: "code",
	})
	if err == nil {
		t.Fatal("expected error from bd.Create failure")
	}
	if !strings.Contains(err.Error(), "create derived bead") {
		t.Errorf("error %v should mention create derived bead", err)
	}
	// Result is non-nil so caller can see partial progress (zero in this
	// case since the first call errored).
	if res == nil {
		t.Fatal("res should not be nil even on error")
	}
}

func TestSpawnFromOutput_MissingArtifact(t *testing.T) {
	d := NewDerivedSpawner(&beads.MockClient{}, newFakeSpawner(), &fakeStore{}, Limits{})
	_, err := d.SpawnFromOutput(context.Background(), SpawnFromOutputRequest{
		OutputPath:  "/tmp/does-not-exist-xyz.json",
		ProjectType: "code",
	})
	if !errors.Is(err, ErrNoArtifact) {
		t.Fatalf("err = %v; want ErrNoArtifact", err)
	}
}

func TestDeriveBranch(t *testing.T) {
	cases := []struct {
		projectType, derivedID, title, want string
	}{
		{"code", "bd-1", "Evaluate library alpha", "derived/evaluate-library-alpha-bd-1"},
		{"code", "bd-2", "  spaces  ", "derived/spaces-bd-2"},
		{"code", "bd-3", "!!!", "derived/bd-3"},
		{"knowledge", "bd-4", "anything", ""},
		{"code", "bd-5", strings.Repeat("a", 100), "derived/" + strings.Repeat("a", 32) + "-bd-5"},
	}
	for _, tc := range cases {
		got := deriveBranch(tc.projectType, tc.derivedID, tc.title)
		if got != tc.want {
			t.Errorf("deriveBranch(%q,%q,%q) = %q; want %q",
				tc.projectType, tc.derivedID, tc.title, got, tc.want)
		}
	}
}

func TestBuildPrompt(t *testing.T) {
	p := buildPrompt(OutputEntry{
		Title:       "Evaluate library alpha",
		Description: "HTTP client, check retries",
		Context:     map[string]interface{}{"library": "alpha"},
	})
	if !strings.Contains(p, "Evaluate library alpha") {
		t.Errorf("prompt missing title: %q", p)
	}
	if !strings.Contains(p, "HTTP client, check retries") {
		t.Errorf("prompt missing description: %q", p)
	}
	if !strings.Contains(p, "library") {
		t.Errorf("prompt missing context key: %q", p)
	}

	// No description / context: still renders without empty sections.
	p2 := buildPrompt(OutputEntry{Title: "x"})
	if strings.Contains(p2, "Description:") {
		t.Errorf("empty description should be omitted: %q", p2)
	}
	if strings.Contains(p2, "Context:") {
		t.Errorf("empty context should be omitted: %q", p2)
	}
}
