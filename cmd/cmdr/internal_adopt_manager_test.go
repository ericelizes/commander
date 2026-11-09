package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/wsmgr"
)

type fakeAdopter struct {
	args []string
	err  error
}

func (f *fakeAdopter) Adopt(_ context.Context, project, bead, window string) (*wsmgr.AdoptResult, error) {
	f.args = []string{project, bead, window}
	if f.err != nil {
		return nil, f.err
	}
	return &wsmgr.AdoptResult{Project: project, BeadID: bead, Role: "manager-" + bead, OldRole: "manager-" + project, Title: "Sample shelf overview", Rank: 2}, nil
}

func useAdopter(t *testing.T, f *fakeAdopter) {
	t.Helper()
	orig := newWorkstreamAdopter
	newWorkstreamAdopter = func() (workstreamAdopter, func(), error) { return f, func() {}, nil }
	t.Cleanup(func() { newWorkstreamAdopter = orig })
}

func TestAdoptManager_AdoptsAndMailsTheLiveManagerItsNewRole(t *testing.T) {
	f := &fakeAdopter{}
	useAdopter(t, f)
	mailer := stubMail(t)
	// the adopted Manager's new role resolves to its (unchanged) am name
	orig := mailDeps
	st := newFakeAmStore()
	st.m[amKey+"\x00commander"] = "CalmCave"
	st.m[amKey+"\x00manager-example-app-t01"] = "GoldTiger"
	mailDeps = func() (amAgentStore, amMailer, string, func(), error) { return st, mailer, amKey, func() {}, nil }
	t.Cleanup(func() { mailDeps = orig })

	stdout, stderr, readOutput := withTempOutputFiles(t)
	code := runInternalAdoptManager([]string{"--project", "example-app", "--bead", "example-app-t01", "--window", "manager (example-app)"}, stdout, stderr)
	out, errOut := readOutput()
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	if strings.Join(f.args, "|") != "example-app|example-app-t01|manager (example-app)" {
		t.Fatalf("args = %v", f.args)
	}
	if len(mailer.sent) != 1 || mailer.sent[0].to[0] != "GoldTiger" {
		t.Fatalf("expected one mail to the adopted Manager, got %+v", mailer.sent)
	}
	body := mailer.sent[0].body
	for _, want := range []string{"manager-example-app-t01", "replaces manager-example-app", "Sample shelf overview"} {
		if !strings.Contains(body, want) {
			t.Errorf("mail body missing %q:\n%s", want, body)
		}
	}
	if !strings.Contains(out, `"manager_role":"manager-example-app-t01"`) {
		t.Fatalf("output: %s", out)
	}
}

func TestAdoptManager_FailureAndMissingFlags(t *testing.T) {
	stdout, stderr, _ := withTempOutputFiles(t)
	if runInternalAdoptManager([]string{"--project", "p"}, stdout, stderr) == 0 {
		t.Fatal("--bead is required")
	}
	useAdopter(t, &fakeAdopter{err: errors.New("no session")})
	stdout, stderr, readOutput := withTempOutputFiles(t)
	if runInternalAdoptManager([]string{"--project", "p", "--bead", "b"}, stdout, stderr) == 0 {
		t.Fatal("expected failure")
	}
	if _, errOut := readOutput(); !strings.Contains(errOut, "no session") {
		t.Fatalf("stderr = %s", errOut)
	}
}

func TestMail_StartsAnUnstartedWorkstreamManager(t *testing.T) {
	f := &fakeOpener{}
	useOpener(t, f)
	origLookup := workstreamForManagerRole
	workstreamForManagerRole = func(_ context.Context, role string) (string, string, bool) {
		return "example-app", strings.TrimPrefix(role, "manager-"), true
	}
	t.Cleanup(func() { workstreamForManagerRole = origLookup })

	st := newFakeAmStore()
	st.m[amKey+"\x00manager-registered"] = "Known"
	_, stderr, _ := withTempOutputFiles(t)
	startMissingManagers(context.Background(), st, amKey, []string{"manager-example-app-t06", "manager-registered", "commander"}, stderr)
	if len(f.opened) != 1 || f.opened[0] != "example-app/example-app-t06" {
		t.Fatalf("only the unregistered workstream Manager should be started, got %v", f.opened)
	}
}
