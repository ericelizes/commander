package chat

import (
	"testing"
	"time"
)

// Nov 19: the Commander's account "personal" was signed out and every turn for
// an hour failed with "OAuth session expired". The failure is recognised; the
// role moves to another logged-in account, never back to a signed-out one, and
// the signed-out account is reported; with none left, nothing moves.
func TestAuthFailureMovesTheCommanderAndReportsTheAccount(t *testing.T) {
	ev := Event{Kind: KindText, Text: "Failed to authenticate: OAuth session expired and could not be refreshed", Error: "authentication_failed"}
	if !authFailure(ev) || !authFailure(Event{Kind: KindResult, Result: &Result{IsError: true, Text: "API Error: 401 not logged in"}}) {
		t.Fatal("auth failure not recognised")
	}
	if authFailure(Event{Kind: KindText, Text: "I could not be refreshed by the docs, sorry"}) || authFailure(Event{Kind: KindResult, Result: &Result{Text: "fine"}}) {
		t.Fatal("ordinary text read as an auth failure")
	}

	var reported [][3]string
	e := New(Options{Home: t.TempDir(), OnAuthFailure: func(role, account, next string) { reported = append(reported, [3]string{role, account, next}) }})
	p := &process{spec: Spec{Runtime: RuntimeClaude, Account: "personal"}}
	c, err := e.chat("commander")
	if err != nil {
		t.Fatal(err)
	}
	c.proc = p
	c.observeAuth(ev)
	if !e.settleAuth(c, p, Event{Kind: KindResult, Result: &Result{IsError: true, Text: ev.Text}}) {
		t.Fatal("the signed-out turn was not handled")
	}
	if len(reported) != 1 || reported[0] != [3]string{"commander", "personal", ""} {
		t.Fatalf("reported %v (no other account is logged in here)", reported)
	}
	if !e.signedOut.has("personal", time.Now()) {
		t.Fatal("the signed-out account is not remembered")
	}
	// A clean turn is not an auth failure; nor is an Operator's (its run recovers itself).
	if e.settleAuth(c, p, Event{Kind: KindResult, Result: &Result{IsError: true}}) {
		t.Fatal("a failure with no auth signal was handled as one")
	}
	op, err := e.chat("operator-x")
	if err != nil {
		t.Fatal(err)
	}
	op.proc = p
	op.observeAuth(ev)
	if e.settleAuth(op, p, Event{Kind: KindResult, Result: &Result{IsError: true}}) {
		t.Fatal("an Operator turn was moved")
	}
}
