package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

// unlinkGraph is one workstream ws-1 with an open user gate ws-1.7 and two
// steps waiting on it: ws-1.8 (closed as superseded) and ws-1.9 (live). A
// second workstream ws-2 has a superseded step that also waits on the gate.
func unlinkGraph() *beads.MockClient {
	return &beads.MockClient{
		ShowResults: map[string]*beads.Bead{
			"ws-1":   {ID: "ws-1", Status: "open"},
			"ws-1.7": {ID: "ws-1.7", Status: "open", Labels: []string{"gate", "approver:user"}},
			"ws-1.8": {ID: "ws-1.8", Status: "closed", CloseReason: "Superseded by ws-1.10"},
			"ws-1.9": {ID: "ws-1.9", Status: "open"},
			"ws-1.5": {ID: "ws-1.5", Status: "open", Labels: []string{"gate", "superseded"}},
			"ws-1.6": {ID: "ws-1.6", Status: "open", Labels: []string{"retired"}},
			"ws-2.3": {ID: "ws-2.3", Status: "closed", Labels: []string{"superseded"}},
			"ws-1.4": {ID: "ws-1.4", Status: "closed", CloseReason: "superseded"},
		},
		DepsResult: map[string][]beads.Dep{
			"ws-1.8": {{IssueID: "ws-1.8", DependsOnID: "ws-1.7", Type: "blocks"}},
			"ws-1.9": {{IssueID: "ws-1.9", DependsOnID: "ws-1.7", Type: "blocks"}},
			"ws-1.6": {{IssueID: "ws-1.6", DependsOnID: "ws-1.7", Type: "blocks"}},
			"ws-1.5": {{IssueID: "ws-1.5", DependsOnID: "ws-1.7", Type: "blocks"}},
			"ws-2.3": {{IssueID: "ws-2.3", DependsOnID: "ws-1.7", Type: "blocks"}},
		},
	}
}

func runUnlink(t *testing.T, mock beads.Client, args ...string) (int, string, string) {
	t.Helper()
	setupObjectiveProject(t, mock)
	stdout, stderr, read := withTempOutputFiles(t)
	code := runInternalGate(append([]string{"unlink", "--project", "manifest"}, args...), stdout, stderr)
	out, errOut := read()
	return code, out, errOut
}

func wroteAny(mock *beads.MockClient) bool {
	calls := strings.Join(mock.Calls, "\n")
	return strings.Contains(calls, "RemoveDep:") || strings.Contains(calls, "AddComment:") ||
		strings.Contains(calls, "SetStatus:") || strings.Contains(calls, "Close")
}

// The owning Manager removes a superseded step's wait; the gate stays open and
// unapproved and the live step's wait is untouched.
func TestGateUnlinkRemovesOnlyTheObsoleteEdge(t *testing.T) {
	mock := unlinkGraph()
	t.Setenv("COMMANDER_ROLE", "manager-ws-1")
	t.Setenv("COMMANDER_MANAGER_ROLE", "")
	code, out, errOut := runUnlink(t, mock, "--bead", "ws-1.7", "--waiter", "ws-1.8,ws-1.6", "--reason", "superseded by the new plan")
	if code != 0 {
		t.Fatalf("exit=%d err=%s", code, errOut)
	}
	var res struct {
		Removed    []string `json:"removed"`
		GateStatus string   `json:"gate_status"`
		Workstream string   `json:"workstream"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if strings.Join(res.Removed, ",") != "ws-1.8,ws-1.6" || res.GateStatus != "open" || res.Workstream != "ws-1" {
		t.Fatalf("result %+v", res)
	}
	calls := strings.Join(mock.Calls, "\n")
	for _, want := range []string{"RemoveDep:ws-1.8->ws-1.7", "RemoveDep:ws-1.6->ws-1.7"} {
		if !strings.Contains(calls, want+"|") {
			t.Fatalf("missing %s in %s", want, calls)
		}
	}
	for _, c := range mock.Calls {
		if strings.HasPrefix(c, "RemoveDep:") && strings.Contains(c, "ws-1.9") {
			t.Fatalf("touched the live step's edge: %s", c)
		}
		if strings.HasPrefix(c, "SetStatus:") || strings.HasPrefix(c, "Close") || strings.HasPrefix(c, "SetLabel:") || strings.HasPrefix(c, "UpdateFields:") {
			t.Fatalf("changed a bead beyond the edge: %s", c)
		}
	}
	if !strings.Contains(calls, "AddComment:ws-1.7") {
		t.Fatalf("no audit note on the gate: %s", calls)
	}
}

func TestGateUnlinkRefusals(t *testing.T) {
	for _, c := range []struct {
		name, role string
		args       []string
		want       string
	}{
		{"operator", "operator-op-x", []string{"--bead", "ws-1.7", "--waiter", "ws-1.8", "--reason", "r"}, "Operators do not change the gate chain"},
		{"live step", "manager-ws-1", []string{"--bead", "ws-1.7", "--waiter", "ws-1.9", "--reason", "r"}, "is not superseded"},
		{"one live step refuses the whole call", "manager-ws-1", []string{"--bead", "ws-1.7", "--waiter", "ws-1.8,ws-1.9", "--reason", "r"}, "is not superseded"},
		{"not a gate", "manager-ws-1", []string{"--bead", "ws-1.9", "--waiter", "ws-1.8", "--reason", "r"}, "is not a gate bead"},
		{"superseded gate", "manager-ws-1", []string{"--bead", "ws-1.5", "--waiter", "ws-1.8", "--reason", "r"}, "only clears obsolete waits on an open gate"},
		{"waiter is a gate", "manager-ws-1", []string{"--bead", "ws-1.7", "--waiter", "ws-1.5", "--reason", "r"}, "gate or track bead"},
		{"other workstream's step", "manager-ws-1", []string{"--bead", "ws-1.7", "--waiter", "ws-2.3", "--reason", "r"}, "is in workstream ws-2"},
		{"no reason", "manager-ws-1", []string{"--bead", "ws-1.7", "--waiter", "ws-1.8"}, "--reason"},
		{"no waiter", "manager-ws-1", []string{"--bead", "ws-1.7", "--reason", "r"}, "--waiter"},
	} {
		t.Run(c.name, func(t *testing.T) {
			mock := unlinkGraph()
			t.Setenv("COMMANDER_ROLE", c.role)
			t.Setenv("COMMANDER_MANAGER_ROLE", "")
			code, _, errOut := runUnlink(t, mock, c.args...)
			if code == 0 || !strings.Contains(errOut, c.want) {
				t.Fatalf("exit=%d err=%s", code, errOut)
			}
			if wroteAny(mock) {
				t.Fatalf("wrote despite refusal: %v", mock.Calls)
			}
		})
	}
}

// Another workstream's Manager gets the exact command for the owning Manager
// and writes nothing; Commander and the user may run it.
func TestGateUnlinkAuthorization(t *testing.T) {
	t.Run("other Manager gets the owning Manager's command", func(t *testing.T) {
		mock := unlinkGraph()
		t.Setenv("COMMANDER_ROLE", "manager-ws-2")
		t.Setenv("COMMANDER_MANAGER_ROLE", "")
		code, _, raw := runUnlink(t, mock, "--bead", "ws-1.7", "--waiter", "ws-1.8", "--reason", "dropped plan")
		var e struct{ Error string }
		_ = json.Unmarshal([]byte(strings.TrimSpace(raw)), &e)
		errOut := e.Error
		want := `cmdr internal-gate unlink --project manifest --bead ws-1.7 --waiter ws-1.8 --reason "dropped plan"`
		if code == 0 || !strings.Contains(errOut, "manager-ws-1") || !strings.Contains(errOut, want) {
			t.Fatalf("exit=%d err=%s", code, errOut)
		}
		if wroteAny(mock) {
			t.Fatalf("wrote: %v", mock.Calls)
		}
	})
	for _, role := range []string{"commander", ""} {
		t.Run("role "+role, func(t *testing.T) {
			mock := unlinkGraph()
			t.Setenv("COMMANDER_ROLE", role)
			t.Setenv("COMMANDER_MANAGER_ROLE", "")
			if code, _, errOut := runUnlink(t, mock, "--bead", "ws-1.7", "--waiter", "ws-1.8", "--reason", "dropped plan"); code != 0 {
				t.Fatalf("exit=%d err=%s", code, errOut)
			}
		})
	}
}

// internal-dep's refusal of a gate edge names the new verb.
func TestInternalDepGateRefusalNamesUnlink(t *testing.T) {
	mock := unlinkGraph()
	setupObjectiveProject(t, mock)
	stdout, stderr, read := withTempOutputFiles(t)
	code := runInternalDep([]string{"--project", "manifest", "rm", "ws-1.8", "ws-1.7"}, stdout, stderr)
	_, errOut := read()
	if code == 0 || !strings.Contains(errOut, "internal-gate unlink") {
		t.Fatalf("exit=%d err=%s", code, errOut)
	}
}

// statefulUnlink keeps the graph in step with the writes (a removed edge is
// gone on the next read, a note shows in the gate's comments) and can fail a
// given RemoveDep call or every AddComment.
type statefulUnlink struct {
	*beads.MockClient
	failRemoveAt int // 1-based RemoveDep call that fails; 0 = none
	removes      int
	commentErr   error
}

func (s *statefulUnlink) RemoveDep(ctx context.Context, dir, issue, dependsOn string) error {
	s.removes++
	if s.removes == s.failRemoveAt {
		s.MockClient.Calls = append(s.MockClient.Calls, "RemoveDepFailed:"+issue)
		return errors.New("bd busy")
	}
	if err := s.MockClient.RemoveDep(ctx, dir, issue, dependsOn); err != nil {
		return err
	}
	s.DepsResult[issue] = slices.DeleteFunc(slices.Clone(s.DepsResult[issue]), func(d beads.Dep) bool { return d.DependsOnID == dependsOn })
	return nil
}

func (s *statefulUnlink) AddComment(ctx context.Context, dir, issue, text string) error {
	if s.commentErr != nil {
		s.MockClient.Calls = append(s.MockClient.Calls, "AddCommentFailed:"+issue)
		return s.commentErr
	}
	_ = s.MockClient.AddComment(ctx, dir, issue, text)
	if s.CommentsResult == nil {
		s.CommentsResult = map[string][]beads.Comment{}
	}
	s.CommentsResult[issue] = append(s.CommentsResult[issue], beads.Comment{Text: text})
	return nil
}

func unlinkResult(t *testing.T, out string) (removed, already []string, warnings []string) {
	t.Helper()
	var res struct {
		Removed  []string `json:"removed"`
		Already  []string `json:"already_removed"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	return res.Removed, res.Already, res.Warnings
}

func countCalls(mock *beads.MockClient, prefix string) int {
	n := 0
	for _, c := range mock.Calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// A RemoveDep that fails after the first removal records the removed edge on
// the gate and says to repeat; the repeat skips the removed edge, clears the
// rest, and a third run is a no-op that exits 0 and writes nothing.
func TestGateUnlinkPartialFailureThenRepeat(t *testing.T) {
	m := &statefulUnlink{MockClient: unlinkGraph(), failRemoveAt: 2}
	t.Setenv("COMMANDER_ROLE", "manager-ws-1")
	t.Setenv("COMMANDER_MANAGER_ROLE", "")
	args := []string{"--bead", "ws-1.7", "--waiter", "ws-1.8,ws-1.6", "--reason", "dropped plan"}
	code, _, errOut := runUnlink(t, m, args...)
	if code == 0 || !strings.Contains(errOut, "failed after 1 of 2") || !strings.Contains(errOut, "Repeat to finish") ||
		!strings.Contains(errOut, "--waiter ws-1.8,ws-1.6") {
		t.Fatalf("first run exit=%d err=%s", code, errOut)
	}
	notes := m.CommentsResult["ws-1.7"]
	if len(notes) != 1 || !strings.Contains(notes[0].Text, "wait of ws-1.8 on this gate") {
		t.Fatalf("removed edge not recorded: %+v", notes)
	}

	m.failRemoveAt = 0
	m.Calls = nil
	code, out, errOut := runUnlink(t, m, args...)
	if code != 0 {
		t.Fatalf("repeat exit=%d err=%s", code, errOut)
	}
	removed, already, _ := unlinkResult(t, out)
	if strings.Join(removed, ",") != "ws-1.6" || strings.Join(already, ",") != "ws-1.8" {
		t.Fatalf("repeat removed=%v already=%v", removed, already)
	}
	if countCalls(m.MockClient, "RemoveDep:") != 1 || len(m.CommentsResult["ws-1.7"]) != 2 ||
		!strings.Contains(m.CommentsResult["ws-1.7"][1].Text, "wait of ws-1.6 on this gate") {
		t.Fatalf("repeat calls=%v notes=%+v", m.Calls, m.CommentsResult["ws-1.7"])
	}

	m.Calls = nil
	code, out, errOut = runUnlink(t, m, args...)
	if code != 0 {
		t.Fatalf("third run exit=%d err=%s", code, errOut)
	}
	removed, already, _ = unlinkResult(t, out)
	if len(removed) != 0 || strings.Join(already, ",") != "ws-1.8,ws-1.6" || wroteAny(m.MockClient) {
		t.Fatalf("third run removed=%v already=%v calls=%v", removed, already, m.Calls)
	}
}

// A failed note leaves a warning (exit 0, edges removed) naming the
// internal-comment command that writes it; until it is written a repeat
// refuses the removed edge (no note names it), and once it is the repeat is a
// no-op.
func TestGateUnlinkNoteFailureNamesTheNoteCommand(t *testing.T) {
	m := &statefulUnlink{MockClient: unlinkGraph(), commentErr: errors.New("bd busy")}
	t.Setenv("COMMANDER_ROLE", "manager-ws-1")
	t.Setenv("COMMANDER_MANAGER_ROLE", "")
	args := []string{"--bead", "ws-1.7", "--waiter", "ws-1.8", "--reason", "dropped plan"}
	code, out, errOut := runUnlink(t, m, args...)
	if code != 0 {
		t.Fatalf("exit=%d err=%s", code, errOut)
	}
	removed, _, warnings := unlinkResult(t, out)
	if strings.Join(removed, ",") != "ws-1.8" || len(warnings) != 1 || !strings.Contains(warnings[0], "note on ws-1.7 failed") ||
		!strings.Contains(warnings[0], "cmdr internal-comment --project manifest --bead ws-1.7 --text") ||
		!strings.Contains(warnings[0], "removed the obsolete wait of ws-1.8 on this gate") {
		t.Fatalf("removed=%v warnings=%v", removed, warnings)
	}
	m.commentErr = nil
	m.Calls = nil
	code, _, errOut = runUnlink(t, m, args...)
	if code == 0 || !strings.Contains(errOut, "ws-1.8 has no wait on ws-1.7") || wroteAny(m.MockClient) {
		t.Fatalf("repeat before the note: exit=%d err=%s calls=%v", code, errOut, m.Calls)
	}
	_ = m.AddComment(context.Background(), "", "ws-1.7", "gate unlink by manager-ws-1: removed the obsolete wait of ws-1.8 on this gate (dropped plan). The gate stays open and unapproved.")
	m.Calls = nil
	code, out, errOut = runUnlink(t, m, args...)
	if code != 0 {
		t.Fatalf("repeat after the note: exit=%d err=%s", code, errOut)
	}
	removed, already, _ := unlinkResult(t, out)
	if len(removed) != 0 || strings.Join(already, ",") != "ws-1.8" || wroteAny(m.MockClient) {
		t.Fatalf("repeat removed=%v already=%v calls=%v", removed, already, m.Calls)
	}
}

// Both the edge removal and the note fail: the error says so.
func TestGateUnlinkPartialFailureAndNoteFailure(t *testing.T) {
	m := &statefulUnlink{MockClient: unlinkGraph(), failRemoveAt: 2, commentErr: errors.New("bd busy")}
	t.Setenv("COMMANDER_ROLE", "manager-ws-1")
	t.Setenv("COMMANDER_MANAGER_ROLE", "")
	code, _, errOut := runUnlink(t, m, "--bead", "ws-1.7", "--waiter", "ws-1.8,ws-1.6", "--reason", "dropped plan")
	if code == 0 || !strings.Contains(errOut, "the note on ws-1.7 failed too") || !strings.Contains(errOut, "cmdr internal-comment --project manifest --bead ws-1.7") {
		t.Fatalf("exit=%d err=%s", code, errOut)
	}
}

// A stage gate made by internal-gate add has no parent: its gate-of: stage
// names the workstream, so that workstream's Manager may unlink and another
// Manager is refused.
func TestGateUnlinkParentlessGateUsesItsStage(t *testing.T) {
	graph := func() *beads.MockClient {
		m := unlinkGraph()
		m.ShowResults["gate-x"] = &beads.Bead{ID: "gate-x", Status: "open", Labels: []string{"gate", "approver:user", "gate-of:ws-1.2"}}
		m.ShowResults["ws-1.2"] = &beads.Bead{ID: "ws-1.2", Status: "open"}
		m.ParentsResult = map[string]string{"gate-x": ""}
		m.DepsResult["ws-1.8"] = []beads.Dep{{IssueID: "ws-1.8", DependsOnID: "gate-x", Type: "blocks"}}
		return m
	}
	t.Run("owning Manager", func(t *testing.T) {
		m := graph()
		t.Setenv("COMMANDER_ROLE", "manager-ws-1")
		t.Setenv("COMMANDER_MANAGER_ROLE", "")
		code, out, errOut := runUnlink(t, m, "--bead", "gate-x", "--waiter", "ws-1.8", "--reason", "dropped plan")
		if code != 0 || !strings.Contains(out, `"workstream":"ws-1"`) {
			t.Fatalf("exit=%d out=%s err=%s", code, out, errOut)
		}
	})
	t.Run("other Manager", func(t *testing.T) {
		m := graph()
		t.Setenv("COMMANDER_ROLE", "manager-ws-2")
		t.Setenv("COMMANDER_MANAGER_ROLE", "")
		code, _, errOut := runUnlink(t, m, "--bead", "gate-x", "--waiter", "ws-1.8", "--reason", "dropped plan")
		if code == 0 || !strings.Contains(errOut, "manager-ws-1") || wroteAny(m) {
			t.Fatalf("exit=%d err=%s calls=%v", code, errOut, m.Calls)
		}
	})
}

// A superseded waiter with no edge on the gate and no unlink note naming it
// is a wrong id: refused before any write, even beside a real waiter.
func TestGateUnlinkRefusesAWaiterWithNoEdge(t *testing.T) {
	for _, waiters := range []string{"ws-1.4", "ws-1.8,ws-1.4"} {
		m := &statefulUnlink{MockClient: unlinkGraph()}
		t.Setenv("COMMANDER_ROLE", "manager-ws-1")
		t.Setenv("COMMANDER_MANAGER_ROLE", "")
		code, _, errOut := runUnlink(t, m, "--bead", "ws-1.7", "--waiter", waiters, "--reason", "dropped plan")
		if code == 0 || !strings.Contains(errOut, "ws-1.4 has no wait on ws-1.7") || !strings.Contains(errOut, "check the id") || wroteAny(m.MockClient) {
			t.Fatalf("%s: exit=%d err=%s calls=%v", waiters, code, errOut, m.Calls)
		}
	}
}

func TestUnlinkNotedParsesBothClauses(t *testing.T) {
	got := unlinkNoted([]beads.Comment{
		{Text: "gate unlink by manager-ws-1: removed the obsolete wait of a, b on this gate; found the obsolete wait of c on this gate already gone (r). The gate stays open and unapproved."},
		{Text: "an unrelated note about the obsolete wait of d on this gate"},
	})
	if !got["a"] || !got["b"] || !got["c"] || got["d"] || len(got) != 3 {
		t.Fatalf("noted %v", got)
	}
}
