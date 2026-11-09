package beads

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestList_ArgvAllFilters(t *testing.T) {
	var got []string
	c := &cmdClient{run: captureExec(&got, []byte("[]"), nil, nil)}

	opts := ListOptions{
		Labels:   []string{"cmdr:brainstorming", "kind:task"},
		LabelAny: []string{"priority:high", "priority:urgent"},
		Status:   "open",
		Type:     "feature",
		Limit:    25,
	}
	if _, err := c.List(context.Background(), "/code/p", opts); err != nil {
		t.Fatalf("List: %v", err)
	}

	want := []string{
		"br", "list", "--json",
		"--label", "cmdr:brainstorming",
		"--label", "kind:task",
		"--label-any", "priority:high",
		"--label-any", "priority:urgent",
		"--status", "open",
		"--type", "feature",
		"--limit", "25",
		"cwd=/code/p",
	}
	assertArgv(t, got, want)
}

func TestList_ArgvMinimal(t *testing.T) {
	var got []string
	c := &cmdClient{run: captureExec(&got, []byte("[]"), nil, nil)}

	if _, err := c.List(context.Background(), "/code/p", ListOptions{}); err != nil {
		t.Fatalf("List: %v", err)
	}

	want := []string{"br", "list", "--json", "cwd=/code/p"}
	assertArgv(t, got, want)
}

func TestList_ParsesJSON(t *testing.T) {
	sample := `[
  {"id":"bd-1","title":"first","status":"open","type":"feature","labels":["cmdr:brainstorming","kind:task"],"priority":1},
  {"id":"bd-2","title":"second","status":"closed","type":"bug","labels":["kind:operational"]}
]`
	c := &cmdClient{run: captureExec(new([]string), []byte(sample), nil, nil)}

	beads, err := c.List(context.Background(), "/code/p", ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(beads) != 2 {
		t.Fatalf("expected 2 beads, got %d", len(beads))
	}
	if beads[0].ID != "bd-1" || beads[0].Title != "first" || beads[0].Status != "open" {
		t.Errorf("bead[0] mismatch: %+v", beads[0])
	}
	if len(beads[0].Labels) != 2 || beads[0].Labels[0] != "cmdr:brainstorming" {
		t.Errorf("bead[0] labels mismatch: %+v", beads[0].Labels)
	}
	if beads[1].ID != "bd-2" {
		t.Errorf("bead[1].ID = %q, want bd-2", beads[1].ID)
	}
}

func TestList_ParsesEnvelopeJSON(t *testing.T) {
	sample := `{
  "issues": [
    {"id":"br-5ko","title":"Fix auth login bug","status":"open","issue_type":"task","labels":["kind:objective"]},
    {"id":"br-idl","title":"Investigate and fix new login bug","status":"open","issue_type":"task","labels":["kind:objective"]}
  ],
  "total": 2
}`
	c := &cmdClient{run: captureExec(new([]string), []byte(sample), nil, nil)}

	beads, err := c.List(context.Background(), "/code/p", ListOptions{Labels: []string{"kind:objective"}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(beads) != 2 {
		t.Fatalf("expected 2 beads, got %d", len(beads))
	}
	if beads[0].ID != "br-5ko" || beads[0].Title != "Fix auth login bug" {
		t.Fatalf("unexpected bead[0]: %+v", beads[0])
	}
}

func TestList_EmptyReturnsNonNilSlice(t *testing.T) {
	cases := map[string]string{
		"empty":      "",
		"empty json": "[]",
		"null":       "null",
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			c := &cmdClient{run: captureExec(new([]string), []byte(out), nil, nil)}
			beads, err := c.List(context.Background(), "/code/p", ListOptions{})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if beads == nil {
				t.Fatal("expected non-nil empty slice")
			}
			if len(beads) != 0 {
				t.Fatalf("expected len 0, got %d", len(beads))
			}
		})
	}
}

// bd keeps only the last --id, so it gets one comma list; br takes the flag repeatedly.
func TestListByIDsPerBackend(t *testing.T) {
	comma := []string{"list", "--json", "--status", "all", "--id", "p-1,p-2"}
	repeated := []string{"list", "--json", "--status", "all", "--id", "p-1", "--id", "p-2"}
	dolt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dolt, ".beads", "dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(dolt, ".worktrees", "op-x")
	if err := os.MkdirAll(filepath.Join(worktree, ".beads"), 0o755); err != nil { // a stale copy, no dolt
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, bin, dir string
		want           []string
	}{
		{"bd", "bd", "/d", comma},
		{"bd by full path", "/opt/bd", "/d", comma},
		{"real br", "br", t.TempDir(), repeated},
		{"br stand-in over a dolt project", "br", dolt, comma},
		{"stand-in in a worktree of one", "/x/br-bd", worktree, comma},
		{"a wrapper with another name", "/x/my-beads", dolt, comma},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("COMMANDER_BEADS_BIN", tc.bin)
			var got []string
			c := &cmdClient{run: func(_ context.Context, _, _ string, args ...string) ([]byte, []byte, error) {
				got = args
				return []byte(`[{"id":"p-1"}]`), nil, nil
			}}
			if _, err := c.List(context.Background(), tc.dir, ListOptions{Status: "all", IDs: []string{"p-1", "p-2"}}); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("args %v, want %v", got, tc.want)
			}
		})
	}
}

// Reverse edges come in either backend's JSON; a failed read is an error, never "none".
func TestDependentsReadsEitherBackendAndFailsClosed(t *testing.T) {
	want := []Dep{{IssueID: "p-2", DependsOnID: "p-1", Type: "blocks"}}
	for name, out := range map[string]string{
		"bd": `[{"id":"p-2","dependency_type":"blocks"}]`,
		"br": `[{"issue_id":"p-2","depends_on_id":"p-1","type":"blocks"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			var got []string
			c := &cmdClient{run: func(_ context.Context, _, _ string, args ...string) ([]byte, []byte, error) {
				got = args
				return []byte(out), nil, nil
			}}
			deps, err := c.Dependents(context.Background(), "/d", "p-1")
			if err != nil || !slices.Equal(deps, want) {
				t.Fatalf("deps %+v err %v", deps, err)
			}
			if !slices.Equal(got, []string{"dep", "list", "p-1", "--direction", "up", "--json"}) {
				t.Fatalf("args %v", got)
			}
		})
	}
	bad := &cmdClient{run: func(context.Context, string, string, ...string) ([]byte, []byte, error) {
		return nil, []byte("busy"), errors.New("exit 1")
	}}
	if _, err := bad.Dependents(context.Background(), "/d", "p-1"); err == nil {
		t.Fatal("a failed read reported no dependents")
	}
}

// Only each backend's own sentence about this very id means gone.
func TestShowSaysNotFoundOnlyForThatBead(t *testing.T) {
	for _, tc := range []struct {
		name, stderr string
		gone         bool
	}{
		{"bd", "Issue p-1 not found\nHint: this ID may have never existed", true},
		{"br", "Error: Issue not found: p-1\nHint: Run 'br list'", true},
		{"another bead", "Issue p-11 not found", false},
		{"busy", "database is locked", false},
		{"silent", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &cmdClient{run: func(context.Context, string, string, ...string) ([]byte, []byte, error) {
				return nil, []byte(tc.stderr), errors.New("exit status 1")
			}}
			_, err := c.Show(context.Background(), "/d", "p-1")
			if err == nil || errors.Is(err, ErrNotFound) != tc.gone {
				t.Fatalf("err %v, gone %v", err, tc.gone)
			}
		})
	}
}

func TestListRetainsOwnEdgesForTheDetailKey(t *testing.T) {
	raw := `[{"id":"p-1","status":"open","dependencies":[{"issue_id":"p-1","depends_on_id":"p-2","type":"blocks"},{"issue_id":"p-1","depends_on_id":"p-root","type":"parent-child"}]}]`
	c := &cmdClient{run: captureExec(new([]string), []byte(raw), nil, nil)}
	got, err := c.List(context.Background(), "/p", ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Dependencies) != 2 || got[0].Dependencies[0].DependsOnID != "p-2" {
		t.Fatalf("edges lost: %+v", got)
	}
	before := detailKey(got[0])
	swapped := got[0]
	swapped.Dependencies = slices.Clone(got[0].Dependencies)
	swapped.Dependencies[0], swapped.Dependencies[1] = swapped.Dependencies[1], swapped.Dependencies[0]
	if detailKey(swapped) != before {
		t.Fatal("edge order changed the key")
	}
	swapped.Dependencies[0].Type = "related"
	if detailKey(swapped) == before {
		t.Fatal("edge type change went unseen")
	}
	if got[0].Dependencies[1].Type != "parent-child" {
		t.Fatal("key generation mutated input")
	}
}
