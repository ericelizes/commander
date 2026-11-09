package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/layout"
	"github.com/example-user/commander/internal/wsmgr"
)

// Tests never touch the real store, mail server or tmux for workstream Managers.
func init() {
	newWorkstreamOpener = func() (workstreamOpener, func(), error) {
		return nil, nil, errors.New("test: no workstream opener")
	}
}

type fakeOpener struct {
	prepared []string
	opened   []string
	opts     []wsmgr.OpenOptions
	err      error
}

func (f *fakeOpener) Open(_ context.Context, project, bead string, opt wsmgr.OpenOptions) (*wsmgr.OpenResult, error) {
	f.opened = append(f.opened, project+"/"+bead)
	f.opts = append(f.opts, opt)
	if f.err != nil {
		return nil, f.err
	}
	return &wsmgr.OpenResult{Project: project, BeadID: wsmgr.Owner(context.Background(), nil, "", bead), Role: "manager-" + wsmgr.Owner(context.Background(), nil, "", bead), Rank: 1, Created: true}, nil
}
func (f *fakeOpener) OpenProject(_ context.Context, project string) (*wsmgr.OpenResult, error) {
	f.opened = append(f.opened, project+"/*")
	return &wsmgr.OpenResult{Project: project}, nil
}
func (f *fakeOpener) PrepareAll(context.Context) ([]layout.WorkstreamWindow, error) { return nil, nil }
func (f *fakeOpener) SyncNames(context.Context) error                               { return nil }
func (f *fakeOpener) Prepare(_ context.Context, t wsmgr.Target) (layout.WorkstreamWindow, error) {
	f.prepared = append(f.prepared, t.BeadID)
	return layout.WorkstreamWindow{BeadID: t.BeadID, AgentName: wsmgr.Role(t.BeadID)}, f.err
}

func useOpener(t *testing.T, f *fakeOpener) {
	t.Helper()
	orig := newWorkstreamOpener
	newWorkstreamOpener = func() (workstreamOpener, func(), error) { return f, func() {}, nil }
	t.Cleanup(func() { newWorkstreamOpener = orig })
}

func TestOpenWorkstream_PassesFlagsAndPrintsResult(t *testing.T) {
	f := &fakeOpener{}
	useOpener(t, f)
	stdout, stderr, readOutput := withTempOutputFiles(t)
	code := runInternalOpenWorkstream([]string{"--project", "example-app", "--bead", "example-app-t11.1", "--step", "example-app-t11.1", "--focus"}, stdout, stderr)
	out, errOut := readOutput()
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	if len(f.opened) != 1 || f.opened[0] != "example-app/example-app-t11.1" || !f.opts[0].Focus || f.opts[0].StepID != "example-app-t11.1" {
		t.Fatalf("opened %v opts %+v", f.opened, f.opts)
	}
	if !strings.Contains(out, `"manager_role":"manager-example-app-t11"`) {
		t.Fatalf("output: %s", out)
	}
}

func TestOpenWorkstream_RequiresFlagsAndReportsErrors(t *testing.T) {
	stdout, stderr, _ := withTempOutputFiles(t)
	if runInternalOpenWorkstream([]string{"--project", "p"}, stdout, stderr) == 0 {
		t.Fatal("--bead is required")
	}
	useOpener(t, &fakeOpener{err: errors.New("boom")})
	stdout, stderr, readOutput := withTempOutputFiles(t)
	if runInternalOpenWorkstream([]string{"--project", "p", "--bead", "b"}, stdout, stderr) == 0 {
		t.Fatal("expected failure")
	}
	if _, errOut := readOutput(); !strings.Contains(errOut, "boom") {
		t.Fatalf("stderr = %s", errOut)
	}
}

func TestCreateObjective_NewWorkstreamGetsAManagerButAStepDoesNot(t *testing.T) {
	f := &fakeOpener{}
	useOpener(t, f)
	mock := &beads.MockClient{CreateID: "obj-9"}
	setupObjectiveProject(t, mock)

	stdout, stderr, _ := withTempOutputFiles(t)
	if code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "A new workstream"}, stdout, stderr); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if len(f.opened) != 1 || f.opened[0] != "manifest/obj-9" {
		t.Fatalf("a new workstream should get its Manager: %v", f.opened)
	}

	f.opened = nil
	stdout, stderr, _ = withTempOutputFiles(t)
	runInternalCreateObjective([]string{"--project", "manifest", "--title", "a step", "--parent", "obj-9"}, stdout, stderr)
	if len(f.opened) != 0 {
		t.Fatalf("steps must not start Managers: %v", f.opened)
	}
}
