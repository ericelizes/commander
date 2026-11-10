package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

type custodySnapshotKey struct{}
type custodyReader struct {
	*beads.MemClient
	fail      string
	readErr   error
	foreign   bool
	checks    int
	snapshots int
}

func (c *custodyReader) ReadCustodySnapshot(ctx context.Context, _ string, read func(context.Context) error) error {
	if ctx.Value(custodySnapshotKey{}) != nil {
		return read(ctx)
	}
	c.snapshots++
	return read(context.WithValue(ctx, custodySnapshotKey{}, true))
}
func (c *custodyReader) RequireLocalCustody(ctx context.Context, _ string, ids []string) error {
	c.checks++
	if ctx.Value(custodySnapshotKey{}) == nil {
		return errors.New("receipt checked outside snapshot")
	}
	if c.foreign {
		return &beads.CrossProjectError{Operation: "receipt", Bead: strings.Join(ids, ","), Err: errors.New("foreign source; owning Manager must establish local custody")}
	}
	return nil
}
func (c *custodyReader) List(ctx context.Context, dir string, o beads.ListOptions) ([]beads.Bead, error) {
	if c.fail == "list" {
		return nil, c.readErr
	}
	if c.fail == "show" {
		return nil, nil
	}
	return c.MemClient.List(ctx, dir, o)
}
func (c *custodyReader) Show(ctx context.Context, dir, id string) (*beads.Bead, error) {
	if c.fail == "show" {
		return nil, c.readErr
	}
	return c.MemClient.Show(ctx, dir, id)
}
func (c *custodyReader) Details(ctx context.Context, dir string, ids []string) (map[string]beads.Detail, error) {
	if c.fail == "details" {
		return nil, c.readErr
	}
	return c.MemClient.Details(ctx, dir, ids)
}
func (c *custodyReader) Dependents(ctx context.Context, dir, id string) ([]beads.Dep, error) {
	if c.fail == "dependents" {
		return nil, c.readErr
	}
	return c.MemClient.Dependents(ctx, dir, id)
}

// Scenario runs the real HTTP write door; injected provenance failures are
// isolated fixtures, never claims of PostgreSQL fixture execution.
func TestCrossProjectCustodyScenario(t *testing.T) {
	var sc struct {
		Cases []struct {
			Name, Fail                   string
			Typed, Foreign, Leave, Allow bool
		}
	}
	raw, err := os.ReadFile("../../tests/evals/scenarios/cross-project-custody-boundary.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	if len(sc.Cases) == 0 {
		t.Fatal("empty scenario")
	}
	for _, tc := range sc.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Setenv("COMMANDER_HOME", t.TempDir())
			c := &custodyReader{MemClient: &beads.MemClient{EdgeParentsOnly: true}, fail: tc.Fail, foreign: tc.Foreign, readErr: errors.New("read unavailable")}
			if tc.Typed {
				c.readErr = fmt.Errorf("wrapped: %w", &beads.CrossProjectError{Operation: "identity/read", Bead: "a", Err: c.readErr})
			}
			c.Seed(beads.Bead{ID: "a", Status: "open"})
			c.Seed(beads.Bead{ID: "b", Status: "open"})
			c.Edges = []beads.Dep{{IssueID: "b", DependsOnID: "a", Type: "blocks"}}
			var ran [][]string
			s := New(Options{Token: testToken, Beads: c, Write: func(_ context.Context, _ string, args []string) ([]byte, []byte, error) {
				ran = append(ran, args)
				return nil, nil, nil
			}})
			ts := httptest.NewServer(s.Handler())
			defer ts.Close()
			in := &beads.DoorIntent{Bead: "a", Verb: "closing prerequisite"}
			if tc.Leave {
				in.LeaveUnheld = "the user takes this work by hand"
			}
			_, out := postWrite(t, ts.URL, testToken, beads.DoorRequest{Dir: projectDir(t), Ops: [][]string{{"close", "a"}}, Intent: in})
			if (out.Exit == 0) != tc.Allow {
				t.Fatalf("exit=%d error=%s writes=%v", out.Exit, out.Error, ran)
			}
			if !tc.Allow && (len(ran) != 0 || !strings.Contains(out.Error, "cross-project")) {
				t.Fatalf("must refuse before any write: %v %s", ran, out.Error)
			}
			if c.snapshots != 1 {
				t.Fatalf("nested load did not reuse snapshot: %d", c.snapshots)
			}
			if tc.Foreign && c.checks != 1 {
				t.Fatalf("foreign provenance not checked: %d", c.checks)
			}
			if tc.Allow && tc.Fail == "" && !tc.Leave && (len(ran) != 2 || ran[0][0] != "update" || ran[0][1] != "b") {
				t.Fatalf("local receipt behavior changed: %v", ran)
			}
		})
	}
}
