package authpause

import (
	"testing"
	"time"
)

func TestIsFailure(t *testing.T) {
	for _, s := range []string{"API Error: 401 OAuth session expired · Please run /login", "Failed to authenticate: authentication_error", "Not logged in"} {
		if !IsFailure(s) {
			t.Errorf("missed %q", s)
		}
	}
	for _, s := range []string{"the tests failed", "rate limit reached", "failed to decode code-mode IPC frame"} {
		if IsFailure(s) {
			t.Errorf("false positive %q", s)
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
	if Recovered(e, true, time.Time{}) || Recovered(e, true, now.Add(-time.Hour)) {
		t.Fatal("the old login counted as a sign-in")
	}
	if !Recovered(e, true, now.Add(time.Minute)) {
		t.Fatal("a changed credential file is a sign-in")
	}
	e.SawLoggedOut = true
	if !Recovered(e, true, time.Time{}) || Recovered(e, false, now.Add(time.Minute)) {
		t.Fatal("logged-out-then-in")
	}
}
