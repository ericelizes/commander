package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/livedata"
)

func TestBrBeadsSingleIDShowAfterList(t *testing.T) {
	var calls []string
	ids := []string{"p-3", "p-1", "p-2"}
	run := func(dir string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "list" {
			return []byte(`{"issues":[{"id":"p-3"},{"id":"p-1"},{"id":"p-2"}]}`), nil
		}
		if len(args) != 3 || args[2] != "--json" {
			return nil, fmt.Errorf("show requires one bead")
		}
		return []byte(fmt.Sprintf(`{"id":%q,"status":"open","dependencies":[{"depends_on_id":"root","type":"parent-child"}]}`, args[1])), nil
	}
	raw, err := brBeads(run, "/p")
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		ID           string
		Dependencies []struct {
			ID   string
			Type string `json:"dependency_type"`
		}
	}
	if err := json.Unmarshal(raw, &got); err != nil || len(got) != len(ids) {
		t.Fatalf("beads = %s, %v", raw, err)
	}
	for i, id := range ids {
		if len(got[i].Dependencies) != 1 || got[i].Dependencies[0].ID != "root" || got[i].Dependencies[0].Type != "parent-child" {
			t.Fatalf("dependencies: %s", raw)
		}
		if got[i].ID != id || calls[i+1] != "show "+id+" --json" {
			t.Fatalf("order: %s; calls %v", raw, calls)
		}
	}
}

func TestBrBeadsPreservesShowError(t *testing.T) {
	want := errors.New("br show p-2 --json: backend unavailable")
	raw, err := brBeads(func(_ string, args ...string) ([]byte, error) {
		if args[0] == "list" {
			return []byte(`[{"id":"p-1"},{"id":"p-2"}]`), nil
		}
		if args[1] == "p-2" {
			return nil, want
		}
		return []byte(`[{"id":"p-1"}]`), nil
	}, "/p")
	if raw != nil || !errors.Is(err, want) {
		t.Fatalf("got %s, %v", raw, err)
	}
}

func TestPrintGraphCheck(t *testing.T) {
	results := []graphWsResult{
		{Project: "p", BeadID: "p-1", Title: "Clean"},
		{Project: "p", BeadID: "p-2", Title: "Off", Mismatches: []graphMismatch{{Bead: "p-2.1", Kind: "status", Detail: "drawn as done, bead is open"}}},
	}
	var out bytes.Buffer
	if n := printGraphCheck(&out, results, false); n != 1 {
		t.Fatalf("mismatches = %d", n)
	}
	for _, want := range []string{"OK        p-1", "MISMATCH  p-2", "[status] p-2.1: drawn as done"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	printGraphCheck(&out, results, true)
	if !strings.Contains(out.String(), `"ok": false`) {
		t.Errorf("json output: %s", out.String())
	}
}

func TestPrintGraphCheckFailsOnInvariantViolation(t *testing.T) {
	results := []graphWsResult{{Project: "p", BeadID: "p-1", Title: "WS", Violations: []livedata.Violation{{BeadID: "p-1.2", Title: "orphan"}}}}
	var out bytes.Buffer
	if n := printGraphCheck(&out, results, false); n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}
	if !strings.Contains(out.String(), "[needs-wait-or-owner] p-1.2: orphan") {
		t.Errorf("output: %s", out.String())
	}
	out.Reset()
	printGraphCheck(&out, results, true)
	if !strings.Contains(out.String(), `"ok": false`) || !strings.Contains(out.String(), `"bead_id": "p-1.2"`) {
		t.Errorf("json: %s", out.String())
	}
}

// An open card drawn outside the You lane fails `cmdr graph check` for the
// workstream, the step and the plain form's audit, and is in the JSON findings.
func TestGraphCheckFailsOnNeedsYouLane(t *testing.T) {
	ask := livedata.SnapStep{BeadID: "w.1", Title: "Approve", Status: "open", State: "needs-user"}
	ws := livedata.SnapWorkstream{N: 1, BeadID: "w", Project: "p", Title: "W", ManagerRole: "manager-w", Steps: []livedata.SnapStep{ask}}
	snap := livedata.Snapshot{Workstreams: []livedata.SnapWorkstream{ws}, NeedsYou: []livedata.NeedsYou{{BeadID: "w.1", Project: "p", Workstream: "W", Title: "Approve"}},
		Agents: map[string]livedata.SnapAgent{"manager-w": {State: "idle"}}}
	now := time.Now().Unix()

	check := func(which string, snap livedata.Snapshot) (int, string) {
		t.Helper()
		dir := t.TempDir()
		body, err := json.Marshal(snap)
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, "snapshot.json")
		if err := os.WriteFile(file, body, 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := os.Create(filepath.Join(dir, "out"))
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		code := runDiagramCheck(which, []string{"--json", "--snapshot", file}, out, os.Stderr)
		got, _ := os.ReadFile(out.Name())
		return code, string(got)
	}

	for _, which := range []string{"w", "w.1"} {
		if code, out := check(which, snap); strings.Contains(out, livedata.CheckNeedsYouLane) {
			t.Fatalf("%s clean board: exit %d, %s", which, code, out)
		}
	}

	d := livedata.Board(snap, now)
	d.Workstreams[0].Diagram.Findings = append(d.Workstreams[0].Diagram.Findings, livedata.DiagramFinding{Check: livedata.CheckNeedsYouLane, Bead: "w.1", Why: "drawn elsewhere", Fix: "fix"})
	snap.Display = &d
	for _, which := range []string{"w", "w.1"} {
		code, out := check(which, snap)
		if code != 1 || !strings.Contains(out, `"check": "`+livedata.CheckNeedsYouLane+`"`) {
			t.Fatalf("%s: exit %d, %s", which, code, out)
		}
	}
	if sus := livedata.AuditSnapshot(snap, now, livedata.AuditOptions{}); len(sus) == 0 || sus[0].Rule != livedata.RuleNeedsYouLane {
		t.Fatalf("audit %+v", sus)
	}
}

// A snapshot that lags a close (the board has not loaded it yet) still has the
// bead open with nothing holding it; the fresh br read says it is closed, so it
// is no hole to report.
func TestGraphCheckDropsViolationsTheFreshReadClosed(t *testing.T) {
	snap := livedata.Snapshot{Workstreams: []livedata.SnapWorkstream{{Project: "p", BeadID: "p-1", Title: "WS", Status: "open", ManagerRole: "manager-p-1",
		Owner: "manager-p-1", Steps: []livedata.SnapStep{
			{BeadID: "p-1.1", Title: "Just closed", Status: "open", State: "not-started"},
			{BeadID: "p-1.2", Title: "Really unheld", Status: "open", State: "not-started"},
			{BeadID: "p-1.3", Title: "Just parked", Status: "open", State: "not-started"},
		}}}}
	vs := livedata.SnapshotViolations(snap)
	ids := func(vs []livedata.Violation) (out []string) {
		for _, v := range vs {
			if v.BeadID != "p-1" {
				out = append(out, v.BeadID)
			}
		}
		return out
	}
	if got := ids(vs); strings.Join(got, ",") != "p-1.1,p-1.2,p-1.3" {
		t.Fatalf("stale snapshot violations = %v", got)
	}
	br := map[string]json.RawMessage{"p": json.RawMessage(`[{"id":"p-1","status":"open"},{"id":"p-1.1","status":"closed"},{"id":"p-1.2","status":"open"},{"id":"p-1.3","status":"deferred"}]`)}
	if got := ids(freshViolations(vs, br)); strings.Join(got, ",") != "p-1.2" {
		t.Fatalf("fresh violations = %v, want only p-1.2", got)
	}
	// Another project's closed bead of the same id excuses nothing.
	other := map[string]json.RawMessage{"q": json.RawMessage(`[{"id":"p-1.2","status":"closed"}]`)}
	if got := ids(freshViolations(vs, other)); len(got) != 3 {
		t.Fatalf("fresh violations with another project's read = %v", got)
	}
}
