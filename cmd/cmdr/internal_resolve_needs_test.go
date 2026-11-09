package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/ownercompat"
	"github.com/example-user/commander/internal/store"
)

// Keep bead reads faithful to successful writes across resolution and refreshes.
type decidedCardClient struct{ beads.MockClient }

func (c *decidedCardClient) SetLabel(ctx context.Context, dir, id, label string, on bool) error {
	if err := c.MockClient.SetLabel(ctx, dir, id, label, on); err != nil {
		return err
	}
	for i := range c.ListResult {
		b := &c.ListResult[i]
		if b.ID != id {
			continue
		}
		b.Labels = slices.DeleteFunc(b.Labels, func(l string) bool { return l == label })
		if on {
			b.Labels = append(b.Labels, label)
		}
	}
	return nil
}
func (c *decidedCardClient) SetStatus(ctx context.Context, dir, id, status string) error {
	if err := c.MockClient.SetStatus(ctx, dir, id, status); err != nil {
		return err
	}
	for i := range c.ListResult {
		if c.ListResult[i].ID == id {
			c.ListResult[i].Status = status
		}
	}
	return nil
}
func (c *decidedCardClient) Close(ctx context.Context, dir, id string) error {
	return c.SetStatus(ctx, dir, id, "closed")
}
func (c *decidedCardClient) Show(_ context.Context, _, id string) (*beads.Bead, error) {
	for i := range c.ListResult {
		if c.ListResult[i].ID == id {
			b := c.ListResult[i]
			return &b, nil
		}
	}
	return nil, nil
}

func TestResolveNeeds_DecidedCardClearsQueueAndCanBeReflagged(t *testing.T) {
	for _, choice := range []string{"Approve", "Change", "Drop", "Other"} {
		for _, flagged := range []bool{false, true} {
			name := choice + "/automatic"
			if flagged {
				name = choice + "/flagged"
			}
			t.Run(name, func(t *testing.T) {
				mailer := stubMail(t)
				labels := []string{"kind:objective", beads.GateLabel, "approver:user"}
				if flagged {
					labels = append(labels, beads.NeedsUserLabel, ownercompat.LegacyNeedsLabel, "waiting:1:user")
				}
				bc := &decidedCardClient{MockClient: beads.MockClient{ListResult: []beads.Bead{
					{ID: "ms-1", Title: "Workstream", Status: "open", Labels: []string{"until:reviewed"}},
					{ID: "ms-1.1", Title: "Workflow approved", Status: "open", Labels: labels, Description: "Stages: build, ship."},
				}}}
				setupObjectiveProject(t, bc)
				ctx := context.Background()
				st, cleanup, err := needsYouStoreFn()
				if err != nil {
					t.Fatal(err)
				}
				defer cleanup()
				p, err := st.GetProjectByName(ctx, "manifest")
				if err != nil {
					t.Fatal(err)
				}
				if flagged {
					if err := st.UpsertNeedsYou(ctx, store.NeedsYou{ProjectID: p.ID, BeadID: "ms-1.1", Ask: "Review workflow", Decision: testDecision, Since: 1}); err != nil {
						t.Fatal(err)
					}
				}
				snapshot := func() livedata.Snapshot {
					t.Helper()
					d := livedata.Load(ctx, livedata.Deps{Store: st, Beads: bc})
					if d.LoadErr != nil {
						t.Fatal(d.LoadErr)
					}
					return livedata.BuildSnapshot(d, nil)
				}
				if len(snapshot().NeedsYou) != 1 {
					t.Fatal("fixture must raise one card")
				}
				stdout, stderr, output := withTempOutputFiles(t)
				if code := runInternalResolveNeeds([]string{"--project", "manifest", "--bead", "ms-1.1", "--action", "decide", "--choice", choice, "--text", "revise stage two"}, stdout, stderr); code != 0 {
					_, errOut := output()
					t.Fatalf("exit %d: %s", code, errOut)
				}
				b, _ := bc.Show(ctx, "", "ms-1.1")
				if slices.Contains(b.Labels, beads.NeedsUserLabel) || slices.Contains(b.Labels, ownercompat.LegacyNeedsLabel) {
					t.Fatalf("ask labels remain: %v", b.Labels)
				}
				if len(beads.WaitsOf(b.Labels)) != 0 {
					t.Fatalf("user wait remains: %v", b.Labels)
				}
				if !slices.Contains(b.Labels, beads.DecisionAnsweredLabel) {
					t.Fatal("missing answered marker")
				}
				wantStatus := "in_progress"
				if choice == "Approve" {
					wantStatus = "closed"
				}
				if choice == "Drop" {
					wantStatus = beads.StatusDeferred
				}
				if b.Status != wantStatus {
					t.Fatalf("status %s, want %s", b.Status, wantStatus)
				}
				if choice == "Change" && !slices.Contains(b.Labels, "owner:manager-ms-1") {
					t.Fatalf("no Manager owner: %v", b.Labels)
				}
				if n, err := st.GetNeedsYou(ctx, p.ID, b.ID); err != nil || n != nil {
					t.Fatalf("stored card remains: %+v %v", n, err)
				}
				for range 2 {
					if s := snapshot(); len(s.NeedsYou) != 0 || len(s.Broken) != 0 {
						t.Fatalf("answered card reappeared or invalid state: %+v", s)
					}
				}
				if len(mailer.sent) != 1 || mailer.sent[0].to[0] != "GoldTiger" || !strings.Contains(strings.ToLower(mailer.sent[0].subject), strings.ToLower(choice)) || !strings.Contains(mailer.sent[0].body, "revise stage two") {
					t.Fatalf("decision mail: %+v", mailer.sent)
				}
				if flagged {
					pitches, err := st.ListPitches(ctx, p.ID, b.ID)
					if err != nil || len(pitches) != 1 || !strings.Contains(pitches[0].Answer, choice) {
						t.Fatalf("answer history: %+v %v", pitches, err)
					}
				}
				fresh := strings.ReplaceAll(testDecision, "Merge #4107?", "Review revised workflow?")
				if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", b.ID, "--status", "open", "--state", "needs-user", "--decision", fresh}, stdout, stderr); code != 0 {
					_, errOut := output()
					t.Fatalf("reflag exit %d: %s", code, errOut)
				}
				b, _ = bc.Show(ctx, "", b.ID)
				if slices.Contains(b.Labels, beads.DecisionAnsweredLabel) {
					t.Fatal("reflag kept answered marker")
				}
				n, err := st.GetNeedsYou(ctx, p.ID, b.ID)
				if err != nil || n == nil || n.Decision != fresh || n.Since == 1 {
					t.Fatalf("fresh card: %+v %v", n, err)
				}
				if s := snapshot(); len(s.NeedsYou) != 1 || s.NeedsYou[0].Decision.Question != "Review revised workflow?" {
					t.Fatalf("fresh snapshot card: %+v", s.NeedsYou)
				}
				mailsBefore := len(mailer.sent)
				if code := runInternalResolveNeeds([]string{"--project", "manifest", "--bead", b.ID, "--action", "decide", "--choice", "Change", "--text", "second answer"}, stdout, stderr); code != 0 {
					t.Fatal("could not resolve re-flagged card")
				}
				r, err := st.GetNeedsResolution(ctx, p.ID, b.ID)
				if err != nil || r == nil || !r.Mailed || r.Choice != "Change" || r.Text != "second answer" || len(mailer.sent) != mailsBefore+1 {
					t.Fatalf("old journal masked fresh answer: %+v %v mails=%d", r, err, len(mailer.sent))
				}
				b, _ = bc.Show(ctx, "", b.ID)
				if !slices.Contains(b.Labels, "owner:manager-ms-1") {
					t.Fatalf("Change must name the owning Manager: %v", b.Labels)
				}
			})
		}
	}
}

const approvesCard = `{"question":"Greenlight the rebuild?","options":[{"label":"Ship it now","approves":true},{"label":"Not yet"}]}`

func approvingChoiceSetup(t *testing.T, card string) (*decidedCardClient, *store.Store, int64) {
	t.Helper()
	bc := &decidedCardClient{MockClient: beads.MockClient{ListResult: []beads.Bead{
		{ID: "ms-1", Title: "Workstream", Status: "open", Labels: []string{"until:reviewed"}},
		{ID: "ms-1.1", Title: "Greenlight: rebuild", Status: "in_progress", Labels: []string{"kind:objective", beads.GateLabel, "approver:user", beads.NeedsUserLabel}},
	}}}
	setupObjectiveProject(t, bc)
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	p, err := st.GetProjectByName(context.Background(), "manifest")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertNeedsYou(context.Background(), store.NeedsYou{ProjectID: p.ID, BeadID: "ms-1.1", Ask: "Greenlight?", Decision: card, Since: 1}); err != nil {
		t.Fatal(err)
	}
	return bc, st, p.ID
}

func TestApprovingChoice_ClosesGate(t *testing.T) {
	cases := []struct {
		choice, card string
		closes       bool
	}{
		{"Ship it now", approvesCard, true},
		{"Not yet", approvesCard, false},
		{"Build it", testDecision, true},
		{"Approve rebuild", testDecision, true},
		{"Go ahead", testDecision, true},
		{"Change", testDecision, false},
		{"Drop", testDecision, false},
		{"Not yet", testDecision, false},
		{"Done", testDecision, false},
	}
	for _, tc := range cases {
		t.Run(tc.choice, func(t *testing.T) {
			mailer := stubMail(t)
			bc, st, pid := approvingChoiceSetup(t, tc.card)
			stdout, stderr, output := withTempOutputFiles(t)
			args := []string{"--project", "manifest", "--bead", "ms-1.1", "--action", "decide", "--choice", tc.choice}
			if code := runInternalResolveNeeds(args, stdout, stderr); code != 0 {
				_, errOut := output()
				t.Fatalf("exit %d: %s", code, errOut)
			}
			b, _ := bc.Show(context.Background(), "", "ms-1.1")
			if got := b.Status == "closed"; got != tc.closes {
				t.Fatalf("closed=%v want %v (status %s)", got, tc.closes, b.Status)
			}
			if !slices.Contains(b.Labels, beads.DecisionAnsweredLabel) {
				t.Fatal("answer not recorded")
			}
			if len(mailer.sent) != 1 || !strings.Contains(mailer.sent[0].body, "answer ") {
				t.Fatalf("mail: %+v", mailer.sent)
			}
			r, err := st.GetNeedsResolution(context.Background(), pid, "ms-1.1")
			if err != nil || r == nil || !r.Mailed {
				t.Fatalf("journal: %+v %v", r, err)
			}
			// Retrying changes nothing and sends nothing more.
			runInternalResolveNeeds(append(args, "--resume-only"), stdout, stderr)
			if len(mailer.sent) != 1 {
				t.Fatalf("retry mailed again: %d", len(mailer.sent))
			}
		})
	}
}

func TestGateCardApproves_ResumeClosesAnsweredOpenGate(t *testing.T) {
	mailer := stubMail(t)
	bc, st, pid := approvingChoiceSetup(t, testDecision)
	ctx := context.Background()
	// The journal an older build left: approving choice, no gate choice, bead still open.
	gen, err := st.NeedsGeneration(ctx, pid, "ms-1.1")
	if err != nil {
		t.Fatal(err)
	}
	r := store.NeedsResolution{ProjectID: pid, BeadID: "ms-1.1", Generation: gen, AnswerID: "a1", Action: "decide", Choice: "Build it", Ask: "Greenlight?", Manager: "GoldTiger"}
	if err := st.SaveNeedsResolution(ctx, r, "Build it"); err != nil {
		t.Fatal(err)
	}
	if err := st.AdvanceNeedsResolution(ctx, r, store.ResolutionMailed); err != nil {
		t.Fatal(err)
	}
	if err := bc.SetLabel(ctx, "", "ms-1.1", beads.DecisionAnsweredLabel, true); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, _ := withTempOutputFiles(t)
	args := []string{"--project", "manifest", "--bead", "ms-1.1", "--action", "decide", "--choice", "Build it", "--resume-only"}
	for range 2 {
		if code := runInternalResolveNeeds(args, stdout, stderr); code != 0 {
			t.Fatal("resume failed")
		}
	}
	b, _ := bc.Show(ctx, "", "ms-1.1")
	if b.Status != "closed" {
		t.Fatalf("resume left the approved gate %s", b.Status)
	}
	if len(mailer.sent) != 0 {
		t.Fatalf("resume mailed again: %d", len(mailer.sent))
	}
}
