package place

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/store"
)

func TestNamesAndLabels(t *testing.T) {
	for _, ok := range []string{"mac:laptop", "mac:a", "mac:work-2"} {
		if !ValidName(ok) {
			t.Errorf("%s refused", ok)
		}
	}
	for _, bad := range []string{"laptop", "mac:", "mac:Laptop", "linux:x", "mac:-x", "mac:a b", "mac:" + strings.Repeat("a", 40)} {
		if ValidName(bad) {
			t.Errorf("%s accepted", bad)
		}
	}
	if p, err := FromLabels([]string{"kind:task", "run-on:mac:laptop"}); p != "mac:laptop" || err != nil {
		t.Fatalf("%q %v", p, err)
	}
	if _, err := FromLabels([]string{"run-on:mac:a", "run-on:mac:b"}); err == nil {
		t.Fatal("two places accepted")
	}
	if err := RefuseAgent(func(k string) string { return map[string]string{"COMMANDER_MANAGER_ROLE": "manager-x"}[k] }); err == nil {
		t.Fatal("agent allowed to pair")
	}
	if err := RefuseAgent(func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
}

func TestPairingCodeIsOneTimeAndExpires(t *testing.T) {
	home := t.TempDir()
	dev := NewDevice("mac:laptop")
	now := time.Unix(1000, 0)
	code, err := RequestPairing(home, dev.Pub, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RequestPairing(home, "not-a-key", now); err == nil {
		t.Fatal("bad key accepted")
	}
	got, err := TakePairing(home, strings.ToLower(code), now.Add(time.Minute))
	if err != nil || got != dev.Pub {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := TakePairing(home, code, now); !errors.Is(err, ErrNoPairing) {
		t.Fatalf("code used twice: %v", err)
	}
	old, _ := RequestPairing(home, dev.Pub, now)
	if _, err := TakePairing(home, old, now.Add(PairingTTL+time.Second)); !errors.Is(err, ErrNoPairing) {
		t.Fatalf("expired code: %v", err)
	}
	if _, err := TakePairing(home, "../../etc", now); !errors.Is(err, ErrNoPairing) {
		t.Fatal("path in a code")
	}
}

func TestChallenge(t *testing.T) {
	dev := NewDevice("mac:laptop")
	other := NewDevice("mac:laptop")
	n := NewNonce()
	sig := SignChallenge(dev.Priv, n, dev.Pub)
	if !VerifyChallenge(dev.Pub, n, sig) {
		t.Fatal("good signature refused")
	}
	if VerifyChallenge(dev.Pub, NewNonce(), sig) {
		t.Fatal("replayed signature accepted")
	}
	if VerifyChallenge(other.Pub, n, sig) {
		t.Fatal("another key accepted")
	}
	forged := dev.Hello(n, "rev1")
	forged.PublicKey = other.Pub // a copied key with no private half
	if VerifyChallenge(forged.PublicKey, n, forged.Signature) {
		t.Fatal("a signature by another key accepted")
	}
}

func TestLiveness(t *testing.T) {
	now := time.Unix(10_000, 0)
	run := store.PlaceRun{Place: "mac:laptop", State: store.PlaceRunning}
	cases := []struct {
		beat time.Duration
		want Liveness
		word string
	}{
		{30 * time.Second, Live, "Operator working on mac:laptop"},
		{4 * time.Minute, Unreachable, "Operator on mac:laptop unreachable 4 min"},
		{16 * time.Minute, Died, "Operator on mac:laptop lost"},
	}
	for _, c := range cases {
		run.LastBeat = now.Add(-c.beat).Unix()
		if got := LivenessOf(run, now); got != c.want {
			t.Errorf("%s: %s want %s", c.beat, got, c.want)
		}
		if got := Words(run, now); got != c.word {
			t.Errorf("%s: %q", c.beat, got)
		}
	}
	off := store.PlaceRun{State: store.PlaceOffered, OfferedAt: now.Add(-time.Minute).Unix()}
	if LivenessOf(off, now) != Offered {
		t.Fatal("fresh offer")
	}
	off.OfferedAt = now.Add(-OfferTimeout - time.Second).Unix()
	if LivenessOf(off, now) != Died {
		t.Fatal("stale offer")
	}
}

// harness is a store with one project, one paired place and an offered run.
type harness struct {
	st    *store.Store
	rec   *Recorder
	svc   *Service
	dev   Device
	now   time.Time
	runID int64
}

const brief = "List the files in ~/Documents/reports and summarize them."

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	h := &harness{st: st, rec: NewRecorder(), dev: NewDevice("mac:laptop"), now: time.Unix(100_000, 0)}
	pid, err := st.InsertProject(ctx, store.Project{Name: "demo", Type: "code", Path: t.TempDir(), Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddPlace(ctx, store.Place{Name: h.dev.Place, Kind: "mac", PublicKey: h.dev.Pub, PairedAt: 1, PairedBy: "user"}); err != nil {
		t.Fatal(err)
	}
	h.rec.Beads["demo-1.1"] = Bead{Title: "Summarize reports", Description: brief}
	h.rec.Beads["demo-1"] = Bead{Title: "Read the reports", Labels: []string{Label("mac:laptop")}}
	h.runID, _ = st.InsertAgentRun(ctx, store.AgentRun{ProjectID: pid, TaskBeadID: "demo-1.1", AgentName: "operator-demo-1.1", SpawnTime: h.now.Unix()})
	if err := st.InsertPlaceRun(ctx, store.PlaceRun{RunID: h.runID, Place: h.dev.Place, StepID: "demo-1", ManagerRole: "manager-demo-1",
		BriefSHA: BriefSHA(brief), OfferedAt: h.now.Unix()}); err != nil {
		t.Fatal(err)
	}
	h.svc = NewService(h.rec.Wire(Deps{Store: st, Revision: "rev1", Now: func() time.Time { return h.now }}))
	return h
}

func (h *harness) connect(t *testing.T) *Conn {
	t.Helper()
	n := NewNonce()
	c, err := h.svc.Hello(context.Background(), n, h.dev.Hello(n, "rev1"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (h *harness) accept(t *testing.T) (*Run, string) {
	t.Helper()
	ctx := context.Background()
	tok, err := h.svc.Accept(ctx, h.dev.Place, h.runID, BriefSHA(brief), "")
	if err != nil {
		t.Fatal(err)
	}
	r, err := h.svc.Authorize(ctx, h.runID, tok)
	if err != nil {
		t.Fatal(err)
	}
	return r, tok
}

func TestHelloRefusals(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	n := NewNonce()
	if _, err := h.svc.Hello(ctx, n, h.dev.Hello(n, "rev0")); err == nil || !strings.Contains(err.Error(), "update cmdr on this Mac") {
		t.Fatalf("version skew: %v", err)
	}
	if _, err := h.svc.Hello(ctx, n, h.dev.Hello(NewNonce(), "rev1")); err == nil {
		t.Fatal("signature over another nonce accepted")
	}
	stranger := NewDevice("mac:laptop")
	if _, err := h.svc.Hello(ctx, n, stranger.Hello(n, "rev1")); err == nil {
		t.Fatal("unpaired key accepted")
	}
	h.st.RevokePlace(ctx, h.dev.Place, 2)
	if _, err := h.svc.Hello(ctx, n, h.dev.Hello(n, "rev1")); err == nil {
		t.Fatal("revoked place accepted")
	}
}

func TestOfferFetchAcceptReportFinish(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c := h.connect(t)
	off := <-c.Send // the waiting offer goes out at hello
	if off.Type != "offer" || off.RunID != h.runID || off.BriefSHA != BriefSHA(brief) || off.TaskID != "demo-1.1" {
		t.Fatalf("offer %+v", off)
	}
	if _, err := h.svc.Fetch(ctx, "mac:other", h.runID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another place fetched it: %v", err)
	}
	info, err := h.svc.Fetch(ctx, h.dev.Place, h.runID)
	if err != nil || info.Brief != brief || info.StepTitle != "Read the reports" || info.ManagerRole != "manager-demo-1" {
		t.Fatalf("fetch %+v %v", info, err)
	}
	if _, err := h.svc.Authorize(ctx, h.runID, "guess"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("token before accept")
	}
	r, tok := h.accept(t)
	if _, err := h.svc.Accept(ctx, h.dev.Place, h.runID, BriefSHA(brief), ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("second accept: %v", err)
	}
	if _, err := h.svc.Fetch(ctx, h.dev.Place, h.runID); !errors.Is(err, ErrConflict) {
		t.Fatalf("fetch of a running run: %v", err)
	}
	if _, err := h.svc.Authorize(ctx, h.runID, tok+"x"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("wrong token")
	}
	if _, err := h.svc.Authorize(ctx, h.runID+1, tok); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("token on another run")
	}
	// Comments: idempotent by client id, as the run's role, on its own task.
	if dup, err := h.svc.Comment(ctx, r, "c1", "progress", false); dup || err != nil {
		t.Fatal(dup, err)
	}
	if dup, _ := h.svc.Comment(ctx, r, "c1", "progress", false); !dup {
		t.Fatal("retry written twice")
	}
	if _, err := h.svc.Comment(ctx, r, "", "x", false); err == nil {
		t.Fatal("no client id")
	}
	if err := h.svc.MailManager(ctx, r, "question", "body"); err != nil {
		t.Fatal(err)
	}
	h.rec.Inboxes["operator-demo-1.1"] = []agentmail.Mail{{From: "manager-demo-1", Subject: "direction"}}
	if m, _ := h.svc.ReadMail(ctx, r); len(m) != 1 {
		t.Fatalf("inbox %+v", m)
	}
	h.now = h.now.Add(time.Minute)
	if err := h.svc.Heartbeat(ctx, r, Beat{Alive: true, Tokens: 1200, Refusals: []string{"sudo ls"}, Usage: []json.RawMessage{json.RawMessage(`{"type":"rate_limit_event"}`)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Comment(ctx, r, "final", "Final report: ...", true); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Exit(ctx, r, "exited", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Authorize(ctx, h.runID, tok); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("token outlived the run")
	}
	if err := h.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	comments, mails, settled := h.rec.Snapshot()
	if len(comments) != 3 || comments[0].Role != "operator-demo-1.1" || comments[0].Bead != "demo-1.1" || !strings.Contains(comments[1].Text, "sudo ls") {
		t.Fatalf("comments %+v", comments)
	}
	if len(mails) != 1 || mails[0].To != "manager-demo-1" || mails[0].From != "operator-demo-1.1" {
		t.Fatalf("mails %+v", mails)
	}
	if len(h.rec.Usage) != 1 || !strings.HasPrefix(h.rec.Usage[0], "mac:laptop ") {
		t.Fatalf("usage %+v", h.rec.Usage)
	}
	if len(settled) != 1 || settled[0].Outcome != Finished {
		t.Fatalf("settled %+v", settled)
	}
	if err := h.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, again := h.rec.Snapshot(); len(again) != 1 {
		t.Fatal("settled twice")
	}
	if run, _ := h.st.GetAgentRun(ctx, h.runID); run.Status != "exited" || run.TokensUsed != 1200 {
		t.Fatalf("agent run %+v", run)
	}
}

func TestBriefEditedAfterTheOfferIsRefused(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.rec.Beads["demo-1.1"] = Bead{Title: "Summarize reports", Description: brief + " Then delete them."}
	if _, err := h.svc.Accept(ctx, h.dev.Place, h.runID, BriefSHA(brief), ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("edited brief: %v", err)
	}
	pr, _ := h.st.GetPlaceRun(ctx, h.runID)
	if pr.State != store.PlaceDeclined {
		t.Fatalf("state %s", pr.State)
	}
	h.svc.Sweep(ctx)
	if _, _, settled := h.rec.Snapshot(); len(settled) != 1 || settled[0].Outcome != Declined {
		t.Fatalf("settled %+v", settled)
	}
}

func TestDeclineAndUnansweredOffer(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.svc.Decline(ctx, "mac:other", h.runID, ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("another place declined it")
	}
	if err := h.svc.Decline(ctx, h.dev.Place, h.runID, "Cancel"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Accept(ctx, h.dev.Place, h.runID, BriefSHA(brief), ""); !errors.Is(err, ErrConflict) {
		t.Fatal("accepted after decline")
	}

	h2 := newHarness(t)
	h2.now = h2.now.Add(OfferTimeout + time.Minute)
	h2.svc.Sweep(ctx)
	if _, _, settled := h2.rec.Snapshot(); len(settled) != 1 || settled[0].Outcome != Declined {
		t.Fatalf("unanswered offer: %+v", settled)
	}
}

func TestUnreachableThenDied(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.accept(t)
	h.now = h.now.Add(5 * time.Minute)
	h.svc.Sweep(ctx)
	h.svc.Sweep(ctx)
	_, mails, settled := h.rec.Snapshot()
	if len(mails) != 1 || !strings.Contains(mails[0].Subject, "unreachable") || len(settled) != 0 {
		t.Fatalf("unreachable: mails %+v settled %+v", mails, settled)
	}
	h.now = h.now.Add(DiedAfter)
	h.svc.Sweep(ctx)
	_, _, settled = h.rec.Snapshot()
	if len(settled) != 1 || settled[0].Outcome != Lost {
		t.Fatalf("died: %+v", settled)
	}
	if run, _ := h.st.GetAgentRun(ctx, h.runID); run.Status == "running" {
		t.Fatal("agent run still running")
	}
}

func TestExitWithoutReportIsLost(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	r, _ := h.accept(t)
	h.svc.Exit(ctx, r, "exited", "claude exited 0")
	h.svc.Sweep(ctx)
	if _, _, settled := h.rec.Snapshot(); len(settled) != 1 || settled[0].Outcome != Lost {
		t.Fatalf("%+v", settled)
	}
}

func TestUsageLimitStopsTheRun(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c := h.connect(t)
	<-c.Send
	r, tok := h.accept(t)
	if err := h.svc.Heartbeat(ctx, r, Beat{Alive: true, UsageLimit: true, Usage: nil}); err != nil {
		t.Fatal(err)
	}
	if m := <-c.Send; m.Type != "stop" || m.RunID != h.runID {
		t.Fatalf("no stop sent: %+v", m)
	}
	pr, _ := h.st.GetPlaceRun(ctx, h.runID)
	if pr.State != store.PlaceStopped || pr.Reason != "usage_limit" {
		t.Fatalf("%+v", pr)
	}
	if _, err := h.svc.Authorize(ctx, h.runID, tok); err == nil {
		t.Fatal("token alive after the stop")
	}
	h.svc.Sweep(ctx)
	if _, _, settled := h.rec.Snapshot(); len(settled) != 1 || settled[0].Outcome != Stopped || settled[0].Why != "usage_limit" {
		t.Fatalf("%+v", settled)
	}
}

func TestStopFromTheServer(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c := h.connect(t)
	<-c.Send
	_, tok := h.accept(t)
	if ok, err := h.svc.Stop(ctx, h.runID, "manager-demo-1", ""); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if m := <-c.Send; m.Type != "stop" || m.By != "manager-demo-1" {
		t.Fatalf("%+v", m)
	}
	if _, err := h.svc.Authorize(ctx, h.runID, tok); err == nil {
		t.Fatal("token alive after the stop")
	}
}

func TestTokenExpires(t *testing.T) {
	h := newHarness(t)
	_, tok := h.accept(t)
	h.now = h.now.Add(TokenTTL + time.Minute)
	if _, err := h.svc.Authorize(context.Background(), h.runID, tok); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("expired token accepted")
	}
}

// Accept re-reads what the offer was made against (review
// commander-il9.206.3.1): the step must still name the place, and a prod
// gate must still be a closed gate approved by the user whose scope is the one
// the dialog showed. Each refusal ends the run declined; nothing is minted.
func TestAcceptRechecksTheStepAndTheProdGate(t *testing.T) {
	const scope = "Read the billing table once."
	gate := Bead{Title: "Prod read", Description: scope, Status: "closed", Labels: []string{"kind:gate", "approver:user"}}
	cases := []struct {
		name  string
		step  []string
		gate  *Bead
		scope string
		ok    bool
	}{
		{"gate holds", []string{Label("mac:laptop")}, &gate, scope, true},
		{"run-on removed", nil, &gate, scope, false},
		{"run-on moved", []string{Label("mac:other")}, &gate, scope, false},
		{"gate reopened", []string{Label("mac:laptop")}, &Bead{Description: scope, Status: "open", Labels: gate.Labels}, scope, false},
		{"approver changed", []string{Label("mac:laptop")}, &Bead{Description: scope, Status: "closed", Labels: []string{"kind:gate", "approver:someone"}}, scope, false},
		{"not a gate", []string{Label("mac:laptop")}, &Bead{Description: scope, Status: "closed", Labels: []string{"approver:user"}}, scope, false},
		{"scope widened", []string{Label("mac:laptop")}, &Bead{Description: scope + " And write it.", Status: "closed", Labels: gate.Labels}, scope, false},
		{"dialog showed another scope", []string{Label("mac:laptop")}, &gate, "something else", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			if _, err := h.st.DB().ExecContext(ctx, `UPDATE place_runs SET prod_gate = 'demo-g' WHERE run_id = ?`, h.runID); err != nil {
				t.Fatal(err)
			}
			h.rec.Beads["demo-1"] = Bead{Title: "Read the reports", Labels: c.step}
			h.rec.Beads["demo-g"] = *c.gate
			tok, err := h.svc.Accept(ctx, h.dev.Place, h.runID, BriefSHA(brief), BriefSHA(c.scope))
			pr, _ := h.st.GetPlaceRun(ctx, h.runID)
			if c.ok {
				if err != nil || tok == "" || pr.State != store.PlaceRunning {
					t.Fatalf("refused: %v (state %s)", err, pr.State)
				}
				return
			}
			if !errors.Is(err, ErrConflict) || tok != "" || pr.State != store.PlaceDeclined {
				t.Fatalf("accepted: tok=%q err=%v state=%s", tok, err, pr.State)
			}
		})
	}
}

// Review commander-il9.206.3.4: a revoked Mac's open socket must take no
// frame after a same-name re-pair, get no offer, and be ended by the sweep.
func TestRevokedConnectionTakesNothingAfterARepair(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	old := h.connect(t)
	<-old.Send // the waiting offer
	if _, err := h.st.RevokePlace(ctx, h.dev.Place, 2); err != nil {
		t.Fatal(err)
	}
	fetch := Message{Type: "fetch", RunID: h.runID}
	if rep := h.svc.Handle(ctx, old, fetch); rep == nil || rep.Type != "error" || !strings.Contains(rep.Error, "revoked or replaced") {
		t.Fatalf("revoked connection fetched: %+v", rep)
	}
	select {
	case <-old.Gone():
	default:
		t.Fatal("revoked connection not retired")
	}
	// Re-paired under the same name with a new key: the old socket still
	// takes nothing (it is bound to its own pairing), the new one works.
	newDev := NewDevice(h.dev.Place)
	if err := h.st.AddPlace(ctx, store.Place{Name: h.dev.Place, Kind: "mac", PublicKey: newDev.Pub, PairedAt: 3, PairedBy: "user"}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []Message{fetch, {Type: "accept", RunID: h.runID, BriefSHA: BriefSHA(brief)}, {Type: "decline", RunID: h.runID}} {
		if rep := h.svc.Handle(ctx, old, m); rep == nil || rep.Type != "error" {
			t.Fatalf("%s from the old pairing answered %+v", m.Type, rep)
		}
	}
	if pr, _ := h.st.GetPlaceRun(ctx, h.runID); pr.State != store.PlaceOffered {
		t.Fatalf("old pairing changed the run: %s", pr.State)
	}
	h.dev = newDev
	fresh := h.connect(t)
	<-fresh.Send
	if rep := h.svc.Handle(ctx, fresh, fetch); rep == nil || rep.Type != "run" {
		t.Fatalf("new pairing refused: %+v", rep)
	}
	// The same key re-paired (a new pairing time) also ends a connection
	// made under the earlier pairing, at the sweep.
	if _, err := h.st.RevokePlace(ctx, h.dev.Place, 4); err != nil {
		t.Fatal(err)
	}
	if err := h.st.AddPlace(ctx, store.Place{Name: h.dev.Place, Kind: "mac", PublicKey: newDev.Pub, PairedAt: 5, PairedBy: "user"}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fresh.Gone():
	default:
		t.Fatal("the sweep left a connection of a replaced pairing")
	}
	if h.svc.Connected(h.dev.Place) {
		t.Fatal("a retired connection still registered")
	}
	if sent, _ := h.svc.Offer(ctx, h.runID); sent {
		t.Fatal("an offer went to a retired connection")
	}
}
