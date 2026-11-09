package main

import (
	"context"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"strings"
	"testing"
)

func TestUnregisterAuthorization(t *testing.T) {
	for _, tc := range []struct {
		role, bead, root, confirm string
		ok                        bool
	}{
		{"manager-demo-root", "demo-task", "demo-root", "", true},
		{"commander", "demo-task", "demo-root", "demo-root", true},
		{"", "demo-task", "demo-root", "demo-root", true},
		{"manager-demo-other", "demo-task", "demo-root", "", false},
		{"operator-demo", "demo-task", "demo-root", "demo-root", false},
		{"commander", "demo-root", "demo-root", "demo-root", false},
		{"commander", "demo-task", "demo-root", "old-root", false},
		{"commander", "demo-task", "", "", false},
	} {
		var owners []string
		if tc.root != "" {
			owners = []string{tc.root}
		}
		if err := authorizeUnregister(tc.role, tc.bead, owners, tc.confirm); (err == nil) != tc.ok {
			t.Errorf("%+v: %v", tc, err)
		}
	}
}

// A stale registration under a nested owner: the nested owner and any owner above
// it may remove it, confirming the nearest owner; the nested owner itself refuses.
func TestUnregister_NestedOwnerRefuses(t *testing.T) {
	owners := []string{beads.NestedA1, beads.NestedA, beads.NestedRoot}
	for _, tc := range []struct {
		role, bead, confirm string
		owners              []string
		ok                  bool
	}{
		{"manager-" + beads.NestedA1, beads.NestedStep, beads.NestedA1, owners, true},
		{"manager-" + beads.NestedRoot, beads.NestedStep, beads.NestedA1, owners, true},
		{"manager-" + beads.NestedB, beads.NestedStep, beads.NestedA1, owners, false},
		{"manager-" + beads.NestedRoot, beads.NestedStep, beads.NestedRoot, owners, false},
		{"commander", beads.NestedA1, beads.NestedA1, []string{beads.NestedA1, beads.NestedA, beads.NestedRoot}, false},
	} {
		if err := authorizeUnregister(tc.role, tc.bead, tc.owners, tc.confirm); (err == nil) != tc.ok {
			t.Errorf("%+v: %v", tc, err)
		}
	}
}

func TestUnregisterWorkstreamCLI(t *testing.T) {
	bc := &beads.MockClient{ParentsResult: map[string]string{"demo-task": "demo-root.8", "demo-root.8": ""}}
	setupObjectiveProject(t, bc)
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx := context.Background()
	p, err := st.GetProjectByName(ctx, "manifest")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertWorkstreamManager(ctx, store.WorkstreamManager{ProjectID: p.ID, WorkstreamBeadID: "demo-task", SessionFile: "/fixture/session"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		role, bead, confirm string
		code                int
		text                string
		present             bool
	}{
		{"manager-demo-other", "demo-task", "", 1, "only the owning", true},
		{"operator-demo", "demo-task", "", 1, "Operators cannot", true},
		{"commander", "demo-root.8", "demo-root.8", 1, "is a root", true},
		{"manager-demo-root.8", "demo-task", "wrong-root", 1, "must match", true},
		{"manager-demo-root.8", "demo-task", "", 0, `"dry_run":true`, true},
		{"manager-demo-root.8", "demo-task", "demo-root.8", 0, `"removed":true`, false},
		{"manager-demo-root.8", "demo-task", "demo-root.8", 0, `"removed":false`, false},
	} {
		t.Setenv("COMMANDER_ROLE", tc.role)
		args := []string{"--project", "manifest", "--bead", tc.bead}
		if tc.confirm != "" {
			args = append(args, "--confirm-root", tc.confirm)
		}
		stdout, stderr, read := withTempOutputFiles(t)
		code := runInternalUnregisterWorkstream(args, stdout, stderr)
		out, e := read()
		if code != tc.code || !strings.Contains(out+e, tc.text) {
			t.Fatalf("%+v: code=%d output=%s %s", tc, code, out, e)
		}
		row, err := st.GetWorkstreamManager(ctx, p.ID, "demo-task")
		if err != nil || (row != nil) != tc.present {
			t.Fatalf("registration=%+v err=%v", row, err)
		}
	}
}
