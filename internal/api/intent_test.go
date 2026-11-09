package api

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

// intentServer is a door over an in-memory beads graph: w holds a (CI wait) and b (waits on a).
func intentServer(t *testing.T) (string, *[][]string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	m := &beads.MemClient{EdgeParentsOnly: true}
	m.Seed(beads.Bead{ID: "w", Title: "Area", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "a", Title: "First", Status: "in_progress", Labels: []string{"waiting:1:ci:7"}})
	m.Seed(beads.Bead{ID: "b", Title: "Then"})
	m.Edges = []beads.Dep{
		{IssueID: "a", DependsOnID: "w", Type: "parent-child"},
		{IssueID: "b", DependsOnID: "w", Type: "parent-child"},
		{IssueID: "b", DependsOnID: "a", Type: "blocks"},
	}
	var mu sync.Mutex
	var ran [][]string
	s := New(Options{Token: testToken, Beads: m, Write: func(_ context.Context, _ string, args []string) ([]byte, []byte, error) {
		mu.Lock()
		ran = append(ran, args)
		mu.Unlock()
		return []byte("ok"), nil, nil
	}})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts.URL, &ran, home
}

// The door's {intent}: it judges the write on fresh reads with the one predicate. An agent's unheld
// result is refused; the user's is never refused: the Manager's receipt goes on what it would leave
// unheld, first (or nothing, with the user's recorded leave_unheld); holder adds run first.
func TestDoorIntent(t *testing.T) {
	closeA := [][]string{{"close", "a"}}
	for _, c := range []struct {
		name  string
		ops   [][]string
		env   []string
		leave string
		ok    bool
		first string
	}{
		{name: "agent leaves b unheld", ops: closeA, env: []string{agent}},
		{name: "user leaves b unheld: receipt first", ops: closeA, ok: true, first: "update"},
		{name: "user with a short reason", ops: closeA, leave: "because"},
		{name: "agent with leave_unheld", ops: closeA, env: []string{agent}, leave: "the user said so earlier"},
		{name: "user with leave_unheld", ops: closeA, leave: "the user takes b by hand", ok: true, first: "close"},
		{name: "hold in the same write, holder first", ops: [][]string{{"close", "a"}, {"update", "b", "--add-label", "waiting:2:ci:9"}}, env: []string{agent}, ok: true, first: "update"},
	} {
		t.Run(c.name, func(t *testing.T) {
			url, ran, home := intentServer(t)
			_, out := postWrite(t, url, testToken, beads.DoorRequest{Dir: projectDir(t), Ops: c.ops, Env: c.env, Intent: &beads.DoorIntent{Bead: "a", Verb: "closing a", LeaveUnheld: c.leave}})
			if c.ok != (out.Exit == 0) {
				t.Fatalf("exit %d error %q", out.Exit, out.Error)
			}
			if !c.ok {
				if len(*ran) != 0 || !strings.Contains(out.Error, "refused:") {
					t.Fatalf("refused write ran %v: %s", *ran, out.Error)
				}
				return
			}
			if (*ran)[0][0] != c.first {
				t.Fatalf("order %v", *ran)
			}
			if c.first == "update" && c.env == nil && (len((*ran)[0]) < 4 || (*ran)[0][1] != "b" || !strings.Contains((*ran)[0][3], "Manager_20to_20start")) {
				t.Fatalf("the user's write did not carry the Manager's receipt on b first: %v", *ran)
			}
			if c.leave != "" {
				raw, _ := os.ReadFile(filepath.Join(home, "logs", "leave-unheld.jsonl"))
				if !strings.Contains(string(raw), "takes b by hand") {
					t.Fatalf("not logged: %s", raw)
				}
				return
			}
			if out.Holders["b"] != "wait" {
				t.Fatalf("holders %v", out.Holders)
			}
		})
	}
}
