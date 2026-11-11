package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/store"
)

type fakeRedirectStore struct {
	changes  []store.PlanChange
	recorded []store.PlanChange
}

func (f *fakeRedirectStore) ChangedExecutionPlans(context.Context) ([]store.PlanChange, error) {
	return f.changes, nil
}

func (f *fakeRedirectStore) RecordExecutionRedirect(_ context.Context, c store.PlanChange, _ string) error {
	f.recorded = append(f.recorded, c)
	return nil
}

func TestExecutionRedirectTellsTheAgentInItsSessionAndRecordsIt(t *testing.T) {
	changed := store.PlanChange{ProjectPath: "/work/app", BeadID: "app-1", Agent: "operator-exec-a", Status: "open", ClaimedRevision: 2, CurrentRevision: 3}
	closed := store.PlanChange{ProjectPath: "/work/app", BeadID: "app-2", Agent: "operator-exec-b", Status: "closed", ClaimedRevision: 4, CurrentRevision: 5}
	fs := &fakeRedirectStore{changes: []store.PlanChange{changed, closed}}
	sent := map[string]string{}
	r := executionRedirector{store: fs, send: func(_ context.Context, role, text string) error {
		sent[role] = text
		return nil
	}}
	r.pass(context.Background())
	if !strings.Contains(sent["operator-exec-a"], "plan revision 2 -> 3") || !strings.Contains(sent["operator-exec-a"], "show app-1") {
		t.Fatalf("changed bead message: %q", sent["operator-exec-a"])
	}
	if !strings.Contains(sent["operator-exec-b"], "is now closed") || !strings.Contains(sent["operator-exec-b"], "Stop this work") {
		t.Fatalf("closed bead message: %q", sent["operator-exec-b"])
	}
	if len(fs.recorded) != 2 {
		t.Fatalf("recorded %d", len(fs.recorded))
	}
}

func TestExecutionRedirectRetriesWhenTheSessionIsUnreachable(t *testing.T) {
	fs := &fakeRedirectStore{changes: []store.PlanChange{{BeadID: "app-1", Agent: "operator-exec-a", Status: "open", ClaimedRevision: 1, CurrentRevision: 2}}}
	r := executionRedirector{store: fs, send: func(context.Context, string, string) error { return errors.New("not running") }}
	r.pass(context.Background())
	if len(fs.recorded) != 0 {
		t.Fatal("an undelivered message was recorded as told")
	}
}
