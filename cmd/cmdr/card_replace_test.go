package main

import (
	"context"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func TestDecisionAloneReplacesTheOpenCard(t *testing.T) {
	setupObjectiveProject(t, &beads.MockClient{})
	ctx := context.Background()
	st, cleanup, _ := needsYouStoreFn()
	defer cleanup()
	p, _ := st.GetProjectByName(ctx, "manifest")

	card := `{"question":"Greenlight merging it?","context":"new plan","options":[{"label":"Approve","recommended":true}]}`
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.4", "--decision", card}, stdout, stderr); code == 0 {
		t.Fatal("a card for a step with none open must be refused")
	}
	if _, errOut := read(); !strings.Contains(errOut, "no open card") {
		t.Fatalf("stderr: %s", errOut)
	}

	if err := st.UpsertNeedsYou(ctx, store.NeedsYou{ProjectID: p.ID, BeadID: "ms-1.4", Ask: "Old ask", Note: "old", Decision: `{"question":"Old?"}`}); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, read = withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.4", "--decision", card}, stdout, stderr); code != 0 {
		_, errOut := read()
		t.Fatalf("replace failed: %s", errOut)
	}
	got, _ := st.GetNeedsYou(ctx, p.ID, "ms-1.4")
	if got == nil || !strings.Contains(got.Decision, "new plan") {
		t.Fatalf("the card was not replaced: %+v", got)
	}
}

func TestStateWriteDecisionAndWaitWithoutCardWritesNothing(t *testing.T) {
	mock := &beads.MockClient{}
	setupObjectiveProject(t, mock)
	stdout, stderr, read := withTempOutputFiles(t)
	code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.4", "--decision", testDecision, "--waiting", "ci:#8"}, stdout, stderr)
	_, errOut := read()
	if code == 0 || !strings.Contains(errOut, "no open card") {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
	for _, call := range mock.Calls {
		if strings.HasPrefix(call, "Set") {
			t.Fatalf("partial write: %s", call)
		}
	}
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	p, err := st.GetProjectByName(context.Background(), "manifest")
	if err != nil {
		t.Fatal(err)
	}
	card, err := st.GetNeedsYou(context.Background(), p.ID, "ms-1.4")
	if err != nil || card != nil {
		t.Fatalf("card=%v err=%v", card, err)
	}
}
