package authpause

import (
	"testing"
	"time"
)

func TestOnlyTheStructuredCodeIsAnAuthFailure(t *testing.T) {
	if !IsFailureCode("authentication_failed") {
		t.Fatal("the runtime's structured code must count")
	}
	// Message text is never a signal: a run may print these while it works.
	for _, s := range []string{"", "401 unauthorized", "Not logged in", "please run /login", "rate_limit", "billing_error"} {
		if IsFailureCode(s) {
			t.Errorf("%q paused dispatch", s)
		}
	}
}

func TestMarkIsOnceAndRecoveryNeedsANewSignIn(t *testing.T) {
	home, now := t.TempDir(), time.Now()
	if first, _ := Mark(home, "", "x", now); !first {
		t.Fatal()
	}
	if first, _ := Mark(home, "default", "y", now); first || len(Active(home)) != 1 {
		t.Fatal("pause not deduplicated")
	}
	e := Active(home)[0]
	if Recovered(e, true, time.Time{}, false) || Recovered(e, true, now.Add(-time.Hour), false) {
		t.Fatal("the old login counted as a sign-in")
	}
	if !Recovered(e, true, now.Add(time.Minute), false) {
		t.Fatal("a changed credential file is a sign-in")
	}
	if !Recovered(e, true, time.Time{}, true) {
		t.Fatal("logged in and healthy must clear the pause")
	}
	if Recovered(e, false, now.Add(time.Minute), true) {
		t.Fatal("a logged-out account is never recovered")
	}
	e.SawLoggedOut = true
	if !Recovered(e, true, time.Time{}, false) || Recovered(e, false, now.Add(time.Minute), false) {
		t.Fatal("logged-out-then-in")
	}
}
