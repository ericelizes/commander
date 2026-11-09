package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/wsmgr"
)

func TestCreateObjective_NestedWorkstreamIsAChild(t *testing.T) {
	mock := &beads.MockClient{CreateID: "ws-1.1"}
	setupObjectiveProject(t, mock)

	stdout, stderr, readOutput := withTempOutputFiles(t)
	code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "audit", "--parent", "ws-1", "--track"}, stdout, stderr)
	out, errOut := readOutput()
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	if !strings.Contains(createCall(mock), "parent=ws-1") || !strings.Contains(createCall(mock), "kind:workstream") || !strings.Contains(createCall(mock), "kind:objective") {
		t.Fatalf("create call: %s", createCall(mock))
	}
	if !strings.Contains(out, `"track":true`) {
		t.Fatalf("output: %s", out)
	}
}

func TestCreateObjective_TrackNeedsAParentAndNoGate(t *testing.T) {
	setupObjectiveProject(t, &beads.MockClient{})
	for _, args := range [][]string{
		{"--project", "manifest", "--title", "audit", "--track"},
		{"--project", "manifest", "--title", "audit", "--parent", "ws-1", "--track", "--gate", "--approver", "user"},
	} {
		stdout, stderr, readOutput := withTempOutputFiles(t)
		if code := runInternalCreateObjective(args, stdout, stderr); code == 0 {
			t.Fatalf("%v: want refusal", args)
		}
		readOutput()
	}
}

func TestSetObjective_TrackAndNoTrack(t *testing.T) {
	mem := &beads.MemClient{Prefix: "ws"}
	ctx := context.Background()
	root, _ := mem.Create(ctx, "", "workstream", beads.CreateOptions{Labels: []string{"kind:objective"}})
	child, _ := mem.Create(ctx, "", "outcome", beads.CreateOptions{Parent: root, Labels: []string{"kind:objective"}})
	setupObjectiveProject(t, mem)

	run := func(args ...string) (int, string) {
		stdout, stderr, readOutput := withTempOutputFiles(t)
		code := runInternalSetObjective(append([]string{"--project", "manifest", "--bead", child}, args...), stdout, stderr)
		_, errOut := readOutput()
		return code, errOut
	}
	if code, e := run("--track"); code != 0 {
		t.Fatalf("--track: %s", e)
	}
	b, _ := mem.Show(ctx, "", child)
	if !slices.Contains(b.Labels, beads.WorkstreamLabel) {
		t.Fatalf("labels=%v", b.Labels)
	}
	if code, e := run("--no-track"); code != 0 {
		t.Fatalf("--no-track: %s", e)
	}
	b, _ = mem.Show(ctx, "", child)
	if beads.IsWorkstream(b.Labels) {
		t.Fatalf("labels=%v", b.Labels)
	}
	// A top-level bead cannot be a track.
	stdout, stderr, readOutput := withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", root, "--track"}, stdout, stderr); code == 0 {
		t.Fatal("a bead with no parent became a track")
	}
	if _, e := readOutput(); !strings.Contains(e, "no parent") {
		t.Fatalf("stderr=%s", e)
	}
}

func TestInternalTrackConvert_MovesUnderWorkstream(t *testing.T) {
	mem := &beads.MemClient{Prefix: "ws", EdgeParentsOnly: true}
	ctx := context.Background()
	ws, _ := mem.Create(ctx, "", "workstream", beads.CreateOptions{Labels: []string{"kind:objective"}})
	other, _ := mem.Create(ctx, "", "other area", beads.CreateOptions{Labels: []string{"kind:task"}})
	setupObjectiveProject(t, mem)

	stdout, stderr, readOutput := withTempOutputFiles(t)
	code := runInternalTrack([]string{"convert", "--project", "manifest", "--bead", other, "--into", ws}, stdout, stderr)
	_, errOut := readOutput()
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	d, _ := mem.Details(ctx, "", []string{other})
	if d[other].Parent != ws {
		t.Fatalf("parent=%q want %q", d[other].Parent, ws)
	}
	b, _ := mem.Show(ctx, "", other)
	if !slices.Contains(b.Labels, beads.WorkstreamLabel) || !slices.Contains(b.Labels, "kind:objective") {
		t.Fatalf("labels=%v", b.Labels)
	}

	// Workstreams nest, and the shape holds.
	convert := func(bead, into string) (int, string) {
		stdout, stderr, readOutput := withTempOutputFiles(t)
		code := runInternalTrack([]string{"convert", "--project", "manifest", "--bead", bead, "--into", into}, stdout, stderr)
		_, e := readOutput()
		return code, e
	}
	third, _ := mem.Create(ctx, "", "third", beads.CreateOptions{})
	if code, e := convert(third, other); code != 0 {
		t.Fatalf("into a nested workstream: %s", e)
	}
	if code, e := convert(other, third); code == 0 || !strings.Contains(e, "descendant") {
		t.Fatalf("under its own descendant: %d %s", code, e)
	}
	step, _ := mem.Create(ctx, "", "a step", beads.CreateOptions{Parent: third, Labels: []string{"kind:objective"}})
	fourth, _ := mem.Create(ctx, "", "fourth", beads.CreateOptions{})
	if code, e := convert(fourth, step); code == 0 || !strings.Contains(e, "is a step") {
		t.Fatalf("under a step: %d %s", code, e)
	}
}

// createCall is the mock's Create call, whatever reads came before it.
func createCall(m *beads.MockClient) string {
	for _, c := range m.Calls {
		if strings.HasPrefix(c, "Create") {
			return c
		}
	}
	return ""
}

func TestCreateObjective_TakeOpenStepsSplitsAWorkstream(t *testing.T) {
	mem := &beads.MemClient{Prefix: "ws", EdgeParentsOnly: true}
	ctx := context.Background()
	root, _ := mem.Create(ctx, "", "workstream", beads.CreateOptions{Labels: []string{"kind:objective", beads.WorkstreamLabel}})
	step, _ := mem.Create(ctx, "", "an open step", beads.CreateOptions{Parent: root, Labels: []string{"kind:objective"}})
	first, _ := mem.Create(ctx, "", "a sibling workstream", beads.CreateOptions{Parent: root, Labels: []string{"kind:objective", beads.WorkstreamLabel}})
	setupObjectiveProject(t, mem)

	create := func(args ...string) (int, string, string) {
		stdout, stderr, readOutput := withTempOutputFiles(t)
		code := runInternalCreateObjective(append([]string{"--project", "manifest", "--parent", root}, args...), stdout, stderr)
		out, e := readOutput()
		return code, out, e
	}
	if code, _, e := create("--title", "second", "--workstream"); code == 0 || !strings.Contains(e, "--take-open-steps") {
		t.Fatalf("beside open steps: %d %s", code, e)
	}
	if code, _, e := create("--title", "third", "--take-open-steps"); code == 0 || !strings.Contains(e, "--workstream") {
		t.Fatalf("without --workstream: %d %s", code, e)
	}
	code, _, e := create("--title", "fourth", "--workstream", "--take-open-steps")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, e)
	}
	d, _ := mem.Details(ctx, "", []string{step, first})
	if d[step].Parent == root || d[step].Parent == "" {
		t.Fatalf("the step stayed at %q", d[step].Parent)
	}
	if d[first].Parent != root {
		t.Fatalf("the sibling workstream moved to %q", d[first].Parent)
	}
}

func TestCreateObjective_TopLevelIsLabelledAWorkstream(t *testing.T) {
	mock := &beads.MockClient{CreateID: "ws-9"}
	setupObjectiveProject(t, mock)
	stdout, stderr, readOutput := withTempOutputFiles(t)
	code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "a new area", "--not-a-gate"}, stdout, stderr)
	_, e := readOutput()
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, e)
	}
	if !strings.Contains(createCall(mock), "kind:workstream") {
		t.Fatalf("create call: %s", createCall(mock))
	}
}

// With nested Managers on, a new nested workstream gets its own Manager at once and
// the owner above it hears; with them off nothing starts (one Manager per root).
func TestCreateNestedWorkstreamStartsItsManager(t *testing.T) {
	for _, on := range []bool{false, true} {
		mem := &beads.MemClient{Prefix: "ws", EdgeParentsOnly: true}
		ctx := context.Background()
		root, _ := mem.Create(ctx, "", "workstream", beads.CreateOptions{Labels: []string{"kind:objective", beads.WorkstreamLabel}})
		setupObjectiveProject(t, mem)
		t.Setenv("COMMANDER_ROLE", "")
		p, err := resolveProject("manifest")
		if err != nil {
			t.Fatal(err)
		}
		if err := beads.SetNestedManagers(p.Path, on); err != nil {
			t.Fatal(err)
		}
		f := &fakeOpener{}
		useOpener(t, f)
		am := newFakeAmStore()
		am.m[amKey+"\x00"+wsmgr.Role(root)] = "RootManager"
		am.m[amKey+"\x00commander"] = "TheCommander"
		mailer := &fakeMailer{}
		orig := mailDeps
		mailDeps = func() (amAgentStore, amMailer, string, func(), error) { return am, mailer, amKey, func() {}, nil }
		t.Cleanup(func() { mailDeps = orig })

		stdout, stderr, readOutput := withTempOutputFiles(t)
		code := runInternalCreateObjective([]string{"--project", "manifest", "--parent", root, "--title", "nested area", "--workstream"}, stdout, stderr)
		out, e := readOutput()
		if code != 0 {
			t.Fatalf("on=%v exit=%d stderr=%s", on, code, e)
		}
		var res struct {
			BeadID string `json:"bead_id"`
		}
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatal(err)
		}
		if !on {
			if len(f.opened) != 0 || len(mailer.sent) != 0 {
				t.Fatalf("switch off: opened %v, mailed %v", f.opened, mailer.sent)
			}
			continue
		}
		if len(f.opened) != 1 || f.opened[0] != "manifest/"+res.BeadID {
			t.Fatalf("opened %v, want the new nested %s", f.opened, res.BeadID)
		}
		if len(mailer.sent) != 1 || !slices.Contains(mailer.sent[0].to, "RootManager") || !strings.Contains(mailer.sent[0].body, wsmgr.Role(res.BeadID)) {
			t.Fatalf("parent told %+v", mailer.sent)
		}
	}
}
