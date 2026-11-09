package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
)

type checkBeads struct {
	beads.Client
	byID map[string]*beads.Bead
	deps map[string][]beads.Dep
	fail bool
}

func (c *checkBeads) Show(_ context.Context, _, id string) (*beads.Bead, error) {
	if c.fail {
		return nil, errors.New("busy")
	}
	return c.byID[id], nil
}

func (c *checkBeads) Deps(_ context.Context, _, id string) ([]beads.Dep, error) {
	if c.fail {
		return nil, errors.New("busy")
	}
	return c.deps[id], nil
}

func doorServer(t *testing.T, cb *checkBeads) (string, *atomic.Int32, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	var ran atomic.Int32
	s := New(Options{Token: testToken, Beads: cb, Write: func(ctx context.Context, dir string, args []string) ([]byte, []byte, error) {
		ran.Add(1)
		return []byte("ok"), nil, nil
	}})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts.URL, &ran, home
}

const (
	agent = "COMMANDER_ROLE=operator-x"
	until = "waiting:1700000000:until:1800000000:"
)

func TestDoorChecks(t *testing.T) {
	open := &beads.Bead{ID: "b-1", Status: "open"}
	cases := []struct {
		name    string
		env     []string
		args    []string
		bead    *beads.Bead
		deps    []beads.Dep
		blocker *beads.Bead
		refuse  string // "" = allowed
	}{
		{"slug until refused", []string{agent}, []string{"update", "b-1", "--add-label", until + "release"}, open, nil, nil, "plain words"},
		{"words until allowed", []string{agent}, []string{"update", "b-1", "--add-label", beads.Wait{Kind: beads.WaitUntil, Since: time.Unix(1700000000, 0), Until: time.Unix(1800000000, 0), Ref: "the 6 AM window opens"}.Label()}, open, nil, nil, ""},
		{"untyped waiting label refused", []string{agent}, []string{"update", "b-1", "--add-label", "waiting:soon"}, open, nil, nil, "not a typed wait"},
		{"review wait without PR refused", []string{agent}, []string{"update", "b-1", "--add-label", "waiting:1700000000:review:bob"}, open, nil, nil, "names no PR"},
		{"review wait with pr label allowed", []string{agent}, []string{"update", "b-1", "--add-label", "waiting:1700000000:review:bob"}, &beads.Bead{ID: "b-1", Labels: []string{"pr:7"}}, nil, nil, ""},
		{"review wait with its pr label removed refused", []string{agent}, []string{"update", "b-1", "--remove-label", "pr:7", "--add-label", "waiting:1700000000:review:bob"}, &beads.Bead{ID: "b-1", Labels: []string{"pr:7"}}, nil, nil, "names no PR"},
		{"review wait with another pr label left allowed", []string{agent}, []string{"update", "b-1", "--remove-label", "pr:7", "--add-label", "waiting:1700000000:review:bob"}, &beads.Bead{ID: "b-1", Labels: []string{"pr:7", "pr:8"}}, nil, nil, ""},
		{"ci wait allowed", []string{agent}, []string{"update", "b-1", "--add-label", "waiting:1700000000:ci:8"}, open, nil, nil, ""},
		{"user exempt", nil, []string{"update", "b-1", "--add-label", "waiting:soon"}, open, nil, nil, ""},
		{"in progress owned by user refused", []string{agent}, []string{"update", "b-1", "--status", "in_progress", "--add-label", "owner:user"}, open, nil, nil, "owner user"},
		{"in progress with the bead's user owner refused", []string{agent}, []string{"update", "b-1", "--status", "in_progress"}, &beads.Bead{ID: "b-1", Labels: []string{"owner:user"}}, nil, nil, "owner user"},
		{"in progress with an operator allowed", []string{agent}, []string{"update", "b-1", "--status", "in_progress", "--add-label", "owner:operator-x"}, open, nil, nil, ""},
		{"in progress, user owner removed, allowed", []string{agent}, []string{"update", "b-1", "--status", "in_progress", "--remove-label", "owner:user", "--add-label", "owner:operator-x"}, &beads.Bead{ID: "b-1", Labels: []string{"owner:user"}}, nil, nil, ""},
		{"blocked with nothing refused", []string{agent}, []string{"update", "b-1", "--add-label", "blocked"}, open, nil, nil, "names nothing"},
		{"blocked with a wait in the same write allowed", []string{agent}, []string{"update", "b-1", "--add-label", "blocked", "--add-label", "waiting:1700000000:ci:8"}, open, nil, nil, ""},
		{"blocked on an open blocker allowed", []string{agent}, []string{"update", "b-1", "--add-label", "blocked"}, open, []beads.Dep{{Type: "blocks", DependsOnID: "b-2"}}, &beads.Bead{ID: "b-2", Status: "open"}, ""},
		{"blocked on a closed blocker refused", []string{agent}, []string{"update", "b-1", "--add-label", "blocked"}, open, []beads.Dep{{Type: "blocks", DependsOnID: "b-2"}}, &beads.Bead{ID: "b-2", Status: "closed"}, "names nothing"},
		{"gate without approver refused", []string{agent}, []string{"create", "Spec", "--labels", "gate", "--acceptance", "x"}, nil, nil, nil, "approver"},
		{"gate without criteria refused", []string{agent}, []string{"create", "Spec", "--labels", "gate,approver:user"}, nil, nil, nil, "--criteria"},
		{"gate complete allowed", []string{agent}, []string{"create", "Spec", "--labels", "gate,approver:user", "--acceptance", "user says go"}, nil, nil, nil, ""},
		{"greenlight not for user refused", []string{agent}, []string{"create", "Greenlight build", "--labels", "gate,approver:reviewer", "--acceptance", "x"}, nil, nil, nil, "Greenlight"},
		{"plain create allowed", []string{agent}, []string{"create", "Task", "--labels", "kind:task"}, nil, nil, nil, ""},
		{"label add checked", []string{agent}, []string{"label", "add", "b-1", "waiting:soon"}, open, nil, nil, "not a typed wait"},
		{"close not checked", []string{agent}, []string{"close", "b-1"}, open, nil, nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cb := &checkBeads{byID: map[string]*beads.Bead{}, deps: map[string][]beads.Dep{}}
			if c.bead != nil {
				cb.byID["b-1"] = c.bead
			}
			cb.deps["b-1"] = c.deps
			if c.blocker != nil {
				cb.byID[c.blocker.ID] = c.blocker
			}
			url, ran, home := doorServer(t, cb)
			_, out := postWrite(t, url, testToken, beads.DoorRequest{Dir: projectDir(t), Args: c.args, Env: c.env})
			if c.refuse != "" {
				if out.Exit != 1 || !strings.Contains(out.Error, c.refuse) || ran.Load() != 0 {
					t.Fatalf("want refusal %q, got %+v ran=%d", c.refuse, out, ran.Load())
				}
				if b, _ := os.ReadFile(filepath.Join(home, "logs", "door-refusals.jsonl")); len(b) == 0 {
					t.Fatal("refusal not logged")
				}
				return
			}
			if out.Exit != 0 || out.Error != "" || ran.Load() != 1 {
				t.Fatalf("want allowed, got %+v ran=%d", out, ran.Load())
			}
		})
	}
}

func TestDoorChecks_KillSwitchAndReadFailure(t *testing.T) {
	bad := []string{"update", "b-1", "--add-label", "waiting:soon"}
	cb := &checkBeads{byID: map[string]*beads.Bead{}, deps: map[string][]beads.Dep{}}
	url, ran, _ := doorServer(t, cb)
	t.Setenv(DoorChecksEnv, "off")
	if _, out := postWrite(t, url, testToken, beads.DoorRequest{Dir: projectDir(t), Args: bad, Env: []string{agent}}); out.Exit != 0 || ran.Load() != 1 {
		t.Fatalf("kill switch: %+v", out)
	}
	t.Setenv(DoorChecksEnv, "")
	// A bead that cannot be read lets a blocked label through.
	cb.fail = true
	if _, out := postWrite(t, url, testToken, beads.DoorRequest{Dir: projectDir(t), Args: []string{"update", "b-1", "--add-label", "blocked"}, Env: []string{agent}}); out.Exit != 0 {
		t.Fatalf("read failure should fail open: %+v", out)
	}
}

// Ops of one request are all checked; one bad op refuses the whole request.
func TestDoorChecks_OpsRefusedWhole(t *testing.T) {
	cb := &checkBeads{byID: map[string]*beads.Bead{"b-1": {ID: "b-1"}}, deps: map[string][]beads.Dep{}}
	url, ran, _ := doorServer(t, cb)
	_, out := postWrite(t, url, testToken, beads.DoorRequest{Dir: projectDir(t), Env: []string{agent}, Ops: [][]string{
		{"update", "b-1", "--priority", "1"},
		{"update", "b-1", "--add-label", "waiting:soon"},
	}})
	if out.Exit != 1 || ran.Load() != 0 {
		t.Fatalf("%+v ran=%d", out, ran.Load())
	}
}

// Every rule that depends on a read lets the write through when the read
// fails (and logs it); with the read ok and the rule broken it refuses.
func TestDoorChecks_ReadFailureFailsOpenPerRule(t *testing.T) {
	cases := []struct {
		name string
		args []string
		bead *beads.Bead
		want string
	}{
		{"review wait", []string{"update", "b-1", "--add-label", "waiting:1700000000:review:bob"}, &beads.Bead{ID: "b-1"}, "names no PR"},
		{"in progress owner", []string{"update", "b-1", "--status", "in_progress"}, &beads.Bead{ID: "b-1", Labels: []string{"owner:user"}}, "owner user"},
		{"blocked", []string{"update", "b-1", "--add-label", "blocked"}, &beads.Bead{ID: "b-1"}, "names nothing"},
	}
	for _, c := range cases {
		t.Run(c.name+" read fails: allowed", func(t *testing.T) {
			cb := &checkBeads{byID: map[string]*beads.Bead{"b-1": c.bead}, deps: map[string][]beads.Dep{}, fail: true}
			url, ran, home := doorServer(t, cb)
			_, out := postWrite(t, url, testToken, beads.DoorRequest{Dir: projectDir(t), Args: c.args, Env: []string{agent}})
			if out.Exit != 0 || out.Error != "" || ran.Load() != 1 {
				t.Fatalf("want allowed, got %+v ran=%d", out, ran.Load())
			}
			if b, _ := os.ReadFile(filepath.Join(home, "logs", "door-errors.jsonl")); len(b) == 0 {
				t.Fatal("fail-open not logged")
			}
		})
		t.Run(c.name+" read ok, rule broken: refused", func(t *testing.T) {
			cb := &checkBeads{byID: map[string]*beads.Bead{"b-1": c.bead}, deps: map[string][]beads.Dep{}}
			url, ran, _ := doorServer(t, cb)
			_, out := postWrite(t, url, testToken, beads.DoorRequest{Dir: projectDir(t), Args: c.args, Env: []string{agent}})
			if out.Exit != 1 || !strings.Contains(out.Error, c.want) || ran.Load() != 0 {
				t.Fatalf("want refusal %q, got %+v ran=%d", c.want, out, ran.Load())
			}
		})
	}
}

func TestDoorChecks_OpsRefusalNamesTheOp(t *testing.T) {
	cb := &checkBeads{byID: map[string]*beads.Bead{"b-1": {ID: "b-1"}}, deps: map[string][]beads.Dep{}}
	url, _, _ := doorServer(t, cb)
	_, out := postWrite(t, url, testToken, beads.DoorRequest{Dir: projectDir(t), Env: []string{agent}, Ops: [][]string{
		{"update", "b-1", "--priority", "1"},
		{"update", "b-1", "--add-label", "waiting:soon"},
	}})
	if !strings.Contains(out.Error, "op 2 of 2") {
		t.Fatalf("%+v", out)
	}
}

func TestDoorFailOpenLogIsRateLimited(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	for i := 0; i < 5; i++ {
		logDoorFailOpen("/p", "rate-limit-test", "read bead", errors.New("busy"))
	}
	b, _ := os.ReadFile(filepath.Join(home, "logs", "door-errors.jsonl"))
	if n := strings.Count(string(b), "\n"); n != 1 {
		t.Fatalf("lines = %d", n)
	}
}

func TestExecRunner_DropsCommanderRole(t *testing.T) {
	t.Setenv("COMMANDER_ROLE", "manager-x")
	t.Setenv("COMMANDER_MANAGER_ROLE", "manager-y")
	script := filepath.Join(t.TempDir(), "cmdr")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'role=%s%s' \"$COMMANDER_ROLE\" \"$COMMANDER_MANAGER_ROLE\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if res := ExecRunner(script)(context.Background(), []string{"x"}); res.Stdout != "role=" {
		t.Fatalf("%+v", res)
	}
}
