package main

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/mailstate"
	"github.com/example-user/commander/internal/rolemail"
	"github.com/example-user/commander/internal/store"
)

const dottedOperator = "operator-op-x-il9.80.4"

// A new Operator is registered under its stored dotted name; mail_role prints
// that name, and either spelling reaches the same agent.
func TestOperatorRole_SpawnPrintsTheRegisteredRole(t *testing.T) {
	sp := &fakeOperatorSpawner{}
	stubSpawnDeps(t, sp, 0, 3)
	useMockBeads(t, &beads.MockClient{CreateID: "ms-1.2.1"})
	stdout, stderr, readOutput := withTempOutputFiles(t)
	if code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", "ms-1.2", "--title", "t", "--prompt", "p"}, stdout, stderr); code != 0 {
		out, errOut := readOutput()
		t.Fatalf("exit=%d %s %s", code, out, errOut)
	}
	out, _ := readOutput()
	if !strings.Contains(out, `"mail_role":"operator-op-t-ms-1.2.1"`) || !strings.Contains(out, `"mail_role_alias":"operator-op-t-ms-1-2-1"`) {
		t.Fatalf("output: %s", out)
	}
}

func TestOperatorRole_DottedSlugEndToEnd(t *testing.T) {
	ctx := context.Background()
	st := newFakeAmStore()
	st.m[amKey+"\x00"+dottedOperator] = "RedFox" // how spawn registers it
	st.m[amKey+"\x00manager-a"] = "GoldTiger"
	m := &fakeMailer{inbox: []agentmail.Mail{{Subject: "hi"}}}
	dashed := "operator-op-x-il9-80-4"

	for _, to := range []string{dottedOperator, dashed} {
		got, err := canonicalRecipients(ctx, st, amKey, []string{to}, io.Discard)
		if err != nil || !slices.Equal(got, []string{dottedOperator}) {
			t.Fatalf("%s resolved to %v, %v", to, got, err)
		}
		if err := sendRoleMail(ctx, st, m, amKey, "manager-a", got, "s", "b", agentmail.SendOptions{}); err != nil {
			t.Fatal(err)
		}
		if last := m.sent[len(m.sent)-1]; !slices.Equal(last.to, []string{"RedFox"}) {
			t.Fatalf("sent to %v", last.to)
		}
		// the Waker and hook check the registered spelling: it is the one signalled
		if _, ok := mailstate.Signals()[dottedOperator]; !ok {
			t.Fatalf("no signal for %s", dottedOperator)
		}
	}
	if _, ok := mailstate.Signals()[dashed]; ok {
		t.Fatal("the dashed spelling must not be signalled")
	}

	// inbox by either spelling is the same agent, and nothing is registered
	for _, who := range []string{dottedOperator, dashed} {
		var fetchedAs string
		mm := &nameRecordingMailer{fakeMailer: m, as: &fetchedAs}
		mails, err := fetchRoleInbox(ctx, st, mm, amKey, who, time.Time{}, agentmail.InboxOptions{})
		if err != nil || len(mails) != 1 || fetchedAs != "RedFox" {
			t.Fatalf("%s: %v %v as %q", who, mails, err, fetchedAs)
		}
	}
	if len(m.registered) != 0 || len(st.m) != 2 {
		t.Fatalf("an agent was auto-registered: %v %v", m.registered, st.m)
	}

	// the Operator sending as itself, in either spelling, keeps its agent
	for _, from := range []string{dottedOperator, dashed} {
		if err := sendRoleMail(ctx, st, m, amKey, from, []string{"manager-a"}, "s", "b", agentmail.SendOptions{}); err != nil {
			t.Fatal(err)
		}
		if last := m.sent[len(m.sent)-1]; last.sender != "RedFox" {
			t.Fatalf("sender %q", last.sender)
		}
	}
	if len(m.registered) != 0 {
		t.Fatalf("an agent was auto-registered: %v", m.registered)
	}
}

func TestOperatorRole_DashedRegistrationFoundByDottedSpelling(t *testing.T) {
	st := newFakeAmStore()
	st.m[amKey+"\x00operator-op-x-il9-80-4"] = "RedFox"
	reg, name, err := rolemail.Registered(context.Background(), st, amKey, dottedOperator)
	if err != nil || reg != "operator-op-x-il9-80-4" || name != "RedFox" {
		t.Fatalf("%q %q %v", reg, name, err)
	}
}

func TestOperatorRole_UnresolvedNeverRegisters(t *testing.T) {
	st := newFakeAmStore()
	st.m[amKey+"\x00operator-op-x-other"] = "BlueLake"
	if _, err := canonicalRecipients(context.Background(), st, amKey, []string{dottedOperator}, io.Discard); err == nil || strings.Contains(err.Error(), "did you mean") {
		t.Fatalf("err = %v", err)
	}
}

func TestOperatorRole_WaitValidationWithLegacyDottedRow(t *testing.T) {
	setupObjectiveProject(t, &beads.MockClient{})
	ctx := context.Background()
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	p, _ := st.GetProjectByName(ctx, "manifest")
	if _, err := st.InsertAgentRun(ctx, store.AgentRun{ProjectID: p.ID, TaskBeadID: "ms-1.2.1", AgentName: dottedOperator, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{dottedOperator, "operator-op-x-il9-80-4", "op-x-il9.80.4"} {
		if err := checkWaitsAreReal(ctx, "manifest", "ms-1.2", []beads.Wait{{Kind: beads.WaitOperator, Ref: op}}); err != nil {
			t.Fatalf("%s: %v", op, err)
		}
	}
}

// nameRecordingMailer notes which am agent an inbox was read for.
type nameRecordingMailer struct {
	*fakeMailer
	as *string
}

func (n *nameRecordingMailer) FetchInbox(ctx context.Context, project, agent string, since time.Time, opts agentmail.InboxOptions) ([]agentmail.Mail, error) {
	*n.as = agent
	return n.fakeMailer.FetchInbox(ctx, project, agent, since, opts)
}
