package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

type resolveFailureClient struct {
	decidedCardClient
	showErr, writeErr, closeErr     error
	closed                          bool
	showAfterClose, writeAfterClose bool
}

func (c *resolveFailureClient) Show(ctx context.Context, dir, id string) (*beads.Bead, error) {
	if c.showErr != nil {
		return nil, c.showErr
	}
	if c.closed && c.showAfterClose {
		return nil, errors.New("read after close")
	}
	return c.decidedCardClient.Show(ctx, dir, id)
}
func (c *resolveFailureClient) SetLabel(ctx context.Context, dir, id, label string, on bool) error {
	if c.writeErr != nil {
		return c.writeErr
	}
	if c.closed && c.writeAfterClose {
		return errors.New("write after close")
	}
	return c.decidedCardClient.SetLabel(ctx, dir, id, label, on)
}
func (c *resolveFailureClient) Close(ctx context.Context, dir, id string) error {
	if c.closeErr != nil {
		return c.closeErr
	}
	c.closed = true
	return c.decidedCardClient.Close(ctx, dir, id)
}

func resolveFailureFixture(t *testing.T) (*resolveFailureClient, *store.Store, int64, *fakeMailer) {
	t.Helper()
	mailer := stubMail(t)
	bc := &resolveFailureClient{decidedCardClient: decidedCardClient{MockClient: beads.MockClient{ListResult: []beads.Bead{
		{ID: "ms-1", Status: "open", Labels: []string{"kind:objective", "until:reviewed"}},
		{ID: "ms-1.1", Status: "open", Labels: []string{"kind:objective", beads.GateLabel, "approver:user", beads.NeedsUserLabel, "waiting:1:user", "waiting:2:review:code"}},
	}}}}
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
	if err := st.UpsertNeedsYou(context.Background(), store.NeedsYou{ProjectID: p.ID, BeadID: "ms-1.1", Ask: "Review workflow", Decision: testDecision}); err != nil {
		t.Fatal(err)
	}
	return bc, st, p.ID, mailer
}
func resolveFailureRun(t *testing.T, resume bool) int {
	t.Helper()
	stdout, stderr, output := withTempOutputFiles(t)
	args := []string{"--project", "manifest", "--bead", "ms-1.1", "--action", "decide", "--choice", "Approve", "--text", "reviewed carefully"}
	if resume {
		args = append(args, "--resume-only")
	}
	rc := runInternalResolveNeeds(args, stdout, stderr)
	if rc != 0 {
		_, e := output()
		t.Log(e)
	}
	return rc
}

func TestResolveNeeds_FailuresRetainAnswerAndRetryOnce(t *testing.T) {
	for _, failure := range []string{"show", "flush", "queue-delete", "queue-delete-cli", "close", "mail"} {
		t.Run(failure, func(t *testing.T) {
			bc, st, pid, mailer := resolveFailureFixture(t)
			ctx := context.Background()
			injected := errors.New("injected failure")
			originalMail := mailDeps
			defer func() { mailDeps = originalMail }()
			switch failure {
			case "show":
				bc.showErr = injected
			case "flush":
				bc.writeErr = injected
			case "close":
				bc.closeErr = injected
			case "queue-delete", "queue-delete-cli":
				if _, err := st.DB().Exec(`CREATE TRIGGER fail_delete BEFORE DELETE ON needs_you BEGIN SELECT RAISE(FAIL, 'injected delete failure'); END`); err != nil {
					t.Fatal(err)
				}
			case "mail":
				mailDeps = func() (amAgentStore, amMailer, string, func(), error) { return nil, nil, "", nil, injected }
			}
			if resolveFailureRun(t, false) == 0 {
				t.Fatal("injected failure must surface")
			}
			r, err := st.GetNeedsResolution(ctx, pid, "ms-1.1")
			if err != nil {
				t.Fatal(err)
			}
			if failure == "show" {
				if r != nil {
					t.Fatal("read failure must precede saving")
				}
			} else {
				if r == nil || r.Choice != "Approve" || r.Text != "reviewed carefully" || r.Mailed {
					t.Fatalf("lost recoverable answer: %+v", r)
				}
				pitches, err := st.ListPitches(ctx, pid, "ms-1.1")
				if err != nil || len(pitches) != 1 || !strings.Contains(pitches[0].Answer, "reviewed carefully") {
					t.Fatalf("lost answer history: %+v %v", pitches, err)
				}
			}
			if strings.HasPrefix(failure, "queue-delete") {
				b, _ := bc.decidedCardClient.Show(ctx, "", "ms-1.1")
				n, err := st.GetNeedsYou(ctx, pid, "ms-1.1")
				if err != nil || n == nil || !slices.Contains(b.Labels, beads.DecisionAnsweredLabel) || slices.Contains(b.Labels, beads.NeedsUserLabel) {
					t.Fatal("queue delete failure must be after Flush")
				}
			}
			if failure != "mail" && bc.closed {
				t.Fatal("released dependent before resolution succeeded")
			}
			if len(mailer.sent) != 0 {
				t.Fatal("mailed before resolution succeeded")
			}
			bc.showErr, bc.writeErr, bc.closeErr = nil, nil, nil
			if strings.HasPrefix(failure, "queue-delete") {
				if _, err := st.DB().Exec(`DROP TRIGGER fail_delete`); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "mail" {
				// Retry only sends mail, without reading or overwriting later Manager edits.
				bc.showErr = injected
				if err := bc.decidedCardClient.SetLabel(ctx, "", "ms-1.1", "waiting:3:review:later", true); err != nil {
					t.Fatal(err)
				}
				mailDeps = originalMail
			}
			if resolveFailureRun(t, failure != "show" && failure != "queue-delete-cli") != 0 {
				t.Fatal("retry failed")
			}
			if resolveFailureRun(t, true) != 0 {
				t.Fatal("repeat retry failed")
			}
			if len(mailer.sent) != 1 {
				t.Fatalf("want one mail, got %+v", mailer.sent)
			}
			r, err = st.GetNeedsResolution(ctx, pid, "ms-1.1")
			if err != nil || r == nil || !r.Applied || !r.Mailed {
				t.Fatalf("incomplete journal: %+v %v", r, err)
			}
			n, err := st.GetNeedsYou(ctx, pid, "ms-1.1")
			if err != nil || n != nil {
				t.Fatalf("queue remains: %+v %v", n, err)
			}
			if !bc.closed {
				t.Fatal("retry did not close gate")
			}
			b, _ := bc.decidedCardClient.Show(ctx, "", "ms-1.1")
			if !slices.Contains(b.Labels, "waiting:2:review:code") {
				t.Fatal("other wait changed")
			}
			if failure == "mail" && !slices.Contains(b.Labels, "waiting:3:review:later") {
				t.Fatal("retry overwrote later edits")
			}
		})
	}
}

func TestResolveNeeds_NoShowOrFlushAfterClose(t *testing.T) {
	bc, _, _, mailer := resolveFailureFixture(t)
	bc.showAfterClose, bc.writeAfterClose = true, true
	if resolveFailureRun(t, false) != 0 {
		t.Fatal("read or flush after close")
	}
	if !bc.closed || len(mailer.sent) != 1 {
		t.Fatal("did not resolve")
	}
	if resolveFailureRun(t, true) != 0 || len(mailer.sent) != 1 {
		t.Fatal("completed resume must be a no-op")
	}
}

func TestResolveNeeds_ResumeOnlyDoesNotInventAnswer(t *testing.T) {
	bc, st, pid, mailer := resolveFailureFixture(t)
	if resolveFailureRun(t, true) != 0 {
		t.Fatal("resume no-op failed")
	}
	r, err := st.GetNeedsResolution(context.Background(), pid, "ms-1.1")
	if err != nil || r != nil || bc.closed || len(mailer.sent) != 0 {
		t.Fatal("resume created an answer")
	}
}

func TestResolveNeeds_CloseReceiptFailureRecoversWithoutCleanup(t *testing.T) {
	for _, reflag := range []bool{false, true} {
		t.Run(map[bool]string{false: "closed", true: "reflagged"}[reflag], func(t *testing.T) {
			bc, st, pid, mailer := resolveFailureFixture(t)
			ctx := context.Background()
			if _, err := st.DB().Exec(`CREATE TRIGGER fail_receipt BEFORE UPDATE OF state ON needs_resolutions WHEN NEW.state = 2 BEGIN SELECT RAISE(FAIL, 'close receipt failed'); END`); err != nil {
				t.Fatal(err)
			}
			if resolveFailureRun(t, false) == 0 || !bc.closed {
				t.Fatal("expected failure after Close")
			}
			r, err := st.GetNeedsResolution(ctx, pid, "ms-1.1")
			if err != nil || r.State != store.ResolutionBeadWritten {
				t.Fatalf("lost cleanup receipt: %+v %v", r, err)
			}
			if _, err := st.DB().Exec(`DROP TRIGGER fail_receipt`); err != nil {
				t.Fatal(err)
			}
			if reflag {
				reflagResolution(t)
			}
			mailsBefore := len(mailer.sent)
			// Recovery can read, but cannot replay any label cleanup or Close.
			bc.writeErr, bc.closeErr = errors.New("must not write labels"), errors.New("must not Close")
			if resolveFailureRun(t, true) != 0 {
				t.Fatal("recovery failed")
			}
			if resolveFailureRun(t, true) != 0 {
				t.Fatal("repeat recovery failed")
			}
			if reflag {
				b, _ := bc.decidedCardClient.Show(ctx, "", "ms-1.1")
				n, err := st.GetNeedsYou(ctx, pid, b.ID)
				if err != nil || n == nil || b.Status != "open" || !slices.Contains(b.Labels, beads.NeedsUserLabel) {
					t.Fatalf("new card touched: %+v %+v %v", b, n, err)
				}
				entries, _ := st.ListNeedsResolutions(ctx, pid, b.ID)
				if entries[0].State != store.ResolutionSuperseded || len(mailer.sent) != mailsBefore {
					t.Fatalf("stale answer replayed: %+v", entries)
				}
			} else if len(mailer.sent) != 1 || !strings.Contains(mailer.sent[0].body, "answer "+r.AnswerID) {
				t.Fatal("recovery must be mail-only")
			}
		})
	}
}

func reflagResolution(t *testing.T) {
	t.Helper()
	stdout, stderr, output := withTempOutputFiles(t)
	fresh := strings.ReplaceAll(testDecision, "Merge #4107?", "Review revised workflow?")
	if runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.1", "--status", "open", "--state", "needs-user", "--decision", fresh}, stdout, stderr) != 0 {
		_, e := output()
		t.Fatal(e)
	}
}

func TestResolveNeeds_PendingMailDoesNotMaskNewGeneration(t *testing.T) {
	_, st, pid, mailer := resolveFailureFixture(t)
	original := mailDeps
	mailDeps = func() (amAgentStore, amMailer, string, func(), error) {
		return nil, nil, "", nil, errors.New("mail offline")
	}
	t.Cleanup(func() { mailDeps = original })
	if resolveFailureRun(t, false) == 0 {
		t.Fatal("mail failure must surface")
	}
	reflagResolution(t)
	mailDeps = original
	stdout, stderr, output := withTempOutputFiles(t)
	if runInternalResolveNeeds([]string{"--project", "manifest", "--bead", "ms-1.1", "--action", "decide", "--choice", "Change", "--text", "new answer"}, stdout, stderr) != 0 {
		_, e := output()
		t.Fatal(e)
	}
	ctx := context.Background()
	entries, err := st.ListNeedsResolutions(ctx, pid, "ms-1.1")
	if err != nil || len(entries) != 2 {
		t.Fatalf("journals: %+v %v", entries, err)
	}
	if entries[0].Generation == entries[1].Generation || entries[0].AnswerID == entries[1].AnswerID || entries[0].State != store.ResolutionSuperseded || !entries[1].Mailed || entries[1].Choice != "Change" {
		t.Fatalf("answers merged: %+v", entries)
	}
	pitches, err := st.ListPitches(ctx, pid, "ms-1.1")
	if err != nil || len(pitches) != 2 || !strings.Contains(pitches[1].Answer, "new answer") || !strings.Contains(pitches[0].Answer, "reviewed carefully") {
		t.Fatalf("history: %+v %v", pitches, err)
	}
	if n, _ := st.GetNeedsYou(ctx, pid, "ms-1.1"); n != nil {
		t.Fatal("new card not cleared")
	}
	if resolveFailureRun(t, true) != 0 || len(mailer.sent) != 1 || !strings.Contains(mailer.sent[0].body, entries[1].AnswerID) {
		t.Fatal("new notification not delivered exactly once")
	}
}

func TestResolveNeeds_DuplicateMailCarriesSameAnswerID(t *testing.T) {
	_, st, pid, mailer := resolveFailureFixture(t)
	if _, err := st.DB().Exec(`CREATE TRIGGER fail_mail_receipt BEFORE UPDATE OF state ON needs_resolutions WHEN NEW.state = 3 BEGIN SELECT RAISE(FAIL, 'mail receipt failed'); END`); err != nil {
		t.Fatal(err)
	}
	if resolveFailureRun(t, false) == 0 || len(mailer.sent) != 1 {
		t.Fatal("expected successful send and failed receipt")
	}
	if _, err := st.DB().Exec(`DROP TRIGGER fail_mail_receipt`); err != nil {
		t.Fatal(err)
	}
	if resolveFailureRun(t, true) != 0 || len(mailer.sent) != 2 {
		t.Fatal("retry must finish notification")
	}
	r, _ := st.GetNeedsResolution(context.Background(), pid, "ms-1.1")
	if r.AnswerID == "" || !strings.Contains(mailer.sent[0].body, "answer "+r.AnswerID) || mailer.sent[0].body != mailer.sent[1].body {
		t.Fatal("duplicate lost stable answer id")
	}
}
