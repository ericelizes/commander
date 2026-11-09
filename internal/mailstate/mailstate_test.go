package mailstate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentmail"
)

func inbox(mails ...agentmail.Mail) Fetcher {
	return FetchFunc(func(context.Context, string) ([]agentmail.Mail, error) { return mails, nil })
}

func ids(mails []agentmail.Mail) []string {
	var out []string
	for _, m := range mails {
		out = append(out, m.ID)
	}
	return out
}

func TestUnread_NoCursorOnlyCountsRecentMail(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	now := time.Now()
	f := inbox(
		agentmail.Mail{ID: "1", SentAt: now.Add(-48 * time.Hour)},
		agentmail.Mail{ID: "3", SentAt: now.Add(-time.Minute)},
		agentmail.Mail{ID: "2", SentAt: now.Add(-2 * time.Minute)},
	)
	got, err := Unread(context.Background(), f, "commander")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"2", "3"}; !equal(ids(got), want) {
		t.Fatalf("got %v, want %v (oldest first, history dropped)", ids(got), want)
	}
}

func TestMarkRead_AdvancesCursorAndNeverGoesBackwards(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	now := time.Now()
	m1 := agentmail.Mail{ID: "9", SentAt: now.Add(-time.Minute)}
	m2 := agentmail.Mail{ID: "10", SentAt: now.Add(-30 * time.Second)}
	m3 := agentmail.Mail{ID: "11", SentAt: now}
	f := inbox(m1, m2, m3)

	if err := MarkRead("manager-x", m2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "mail-cursors", "manager-x.json")); err != nil {
		t.Fatalf("cursor file: %v", err)
	}
	got, _ := Unread(context.Background(), f, "manager-x")
	if !equal(ids(got), []string{"11"}) { // numeric compare: 10 > 9, 11 > 10
		t.Fatalf("got %v", ids(got))
	}
	if err := MarkRead("manager-x", m1); err != nil { // stale: must not rewind
		t.Fatal(err)
	}
	got, _ = Unread(context.Background(), f, "manager-x")
	if !equal(ids(got), []string{"11"}) {
		t.Fatalf("cursor moved backwards: %v", ids(got))
	}
	if err := MarkAllRead("manager-x", []agentmail.Mail{m3, m1}); err != nil {
		t.Fatal(err)
	}
	if n, _ := Count(context.Background(), f, "manager-x"); n != 0 {
		t.Fatalf("count after MarkAllRead = %d", n)
	}
}

func TestUnread_CursorsArePerRole(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	m := agentmail.Mail{ID: "5", SentAt: time.Now()}
	f := inbox(m)
	_ = MarkRead("commander", m)
	if n, _ := Count(context.Background(), f, "commander"); n != 0 {
		t.Fatalf("commander count = %d", n)
	}
	if n, _ := Count(context.Background(), f, "manager-a"); n != 1 {
		t.Fatalf("manager-a count = %d", n)
	}
}

func TestUnread_NonNumericIDsFallBackToSendTime(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	now := time.Now()
	a := agentmail.Mail{ID: "a", SentAt: now.Add(-time.Minute)}
	b := agentmail.Mail{ID: "b", SentAt: now}
	_ = MarkRead("commander", a)
	got, _ := Unread(context.Background(), inbox(a, b), "commander")
	if !equal(ids(got), []string{"b"}) {
		t.Fatalf("got %v", ids(got))
	}
}

func TestIsUnread(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	m := agentmail.Mail{ID: "4", SentAt: time.Now()}
	if !IsUnread("commander", m) {
		t.Fatal("fresh mail with no cursor is unread")
	}
	_ = MarkRead("commander", m)
	if IsUnread("commander", m) {
		t.Fatal("marked mail is read")
	}
}

func TestUnread_FetchErrorPropagates(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	f := FetchFunc(func(context.Context, string) ([]agentmail.Mail, error) { return nil, errors.New("down") })
	if _, err := Unread(context.Background(), f, "commander"); err == nil {
		t.Fatal("want error")
	}
}

func TestInvalidRoleRejected(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	if err := MarkRead("../evil", agentmail.Mail{ID: "1"}); err == nil {
		t.Fatal("want error for path-like role")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNeedsCheck_FollowsSignalsAfterTheLastRead(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	if !NeedsCheck("manager-x") {
		t.Fatal("a role never read must be checked")
	}
	read := time.Now()
	if err := MarkChecked("manager-x", read); err != nil {
		t.Fatal(err)
	}
	if NeedsCheck("manager-x") {
		t.Fatal("no mail signalled since the read: nothing to check")
	}
	Signal("manager-x")
	if !NeedsCheck("manager-x") {
		t.Fatal("mail signalled after the read must be checked")
	}
	if got := Signals()["manager-x"]; got.Before(read) {
		t.Fatalf("Signals = %v, want after %v", got, read)
	}
	if err := MarkChecked("manager-x", time.Now()); err != nil {
		t.Fatal(err)
	}
	if NeedsCheck("manager-x") {
		t.Fatal("read after the signal: nothing to check")
	}
}

func TestIsAnswerAndMentionsAnswer(t *testing.T) {
	for subject, want := range map[string][2]bool{
		"[decision] x-1: Merge":          {true, true},
		"[workflow approve] x-1":         {true, true},
		"the user approved: Ship":        {true, true},
		"the user deferred: Ship":        {true, true},
		"Re: [decision] x-1: Merge":      {false, true},
		"[no Manager] [workflow drop] x": {false, true},
		"[decisions] notes":              {false, false},
		"[wait] time reached":            {false, false},
		"The user approved: not a card":  {false, false},
	} {
		if got := [2]bool{IsAnswer(subject), MentionsAnswer(subject)}; got != want {
			t.Errorf("%q: got %v, want %v", subject, got, want)
		}
	}
}

// With no cursor, age hides ordinary Manager mail only: not an answer, and not
// an Operator's mail, which may wait out a long tool call.
func TestUnread_AgeHidesNoAnswerAndNoOperatorMail(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	old := time.Now().Add(-48 * time.Hour)
	mails := []agentmail.Mail{{ID: "1", Subject: "ordinary", SentAt: old}, {ID: "2", Subject: "[decision] x-1: Merge", SentAt: old}}
	got, err := Unread(context.Background(), inbox(mails...), "manager-x")
	if err != nil || !equal(ids(got), []string{"2"}) || HiddenByAge("manager-x", mails) != 1 {
		t.Fatalf("manager: %v %v hidden %d", ids(got), err, HiddenByAge("manager-x", mails))
	}
	got, err = Unread(context.Background(), inbox(mails...), "operator-x")
	if err != nil || !equal(ids(got), []string{"1", "2"}) || HiddenByAge("operator-x", mails) != 0 {
		t.Fatalf("operator: %v %v hidden %d", ids(got), err, HiddenByAge("operator-x", mails))
	}
}

// A failed read holds the hook back for ReadBackoff and strands nothing: the role
// still needs its read once that has passed, and an idle role never needed one.
func TestNeedsCheck_FailedReadBacksOff(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	now := time.Now()
	Now = func() time.Time { return now }
	t.Cleanup(func() { Now = time.Now })
	const role = "operator-x"
	Signal(role)
	if !NeedsCheck(role) {
		t.Fatal("signalled role needs a read")
	}
	if err := MarkReadFailed(role, now); err != nil {
		t.Fatal(err)
	}
	if NeedsCheck(role) {
		t.Fatal("a failed read must back off")
	}
	now = now.Add(ReadBackoff)
	if !NeedsCheck(role) {
		t.Fatal("the backoff passed: the mail must be read again")
	}
}

// An Operator's cursorless history starts at its run, less FirstSightWindow; with
// no run recorded nothing is history. Answers come through at any age.
func TestRunStartBoundsACursorlessOperator(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	const role = "operator-x"
	start := time.Now()
	mails := []agentmail.Mail{
		{ID: "1", Subject: "earlier run", SentAt: start.Add(-FirstSightWindow - time.Minute)},
		{ID: "2", Subject: "[decision] x: Merge", SentAt: start.Add(-48 * time.Hour)},
		{ID: "3", Subject: "while down", SentAt: start.Add(-time.Minute)},
	}
	unread := func() int {
		n, err := Count(context.Background(), FetchFunc(func(context.Context, string) ([]agentmail.Mail, error) { return mails, nil }), role)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := unread(); got != 3 || HiddenByAge(role, mails) != 0 {
		t.Fatalf("no run recorded: unread=%d hidden=%d", got, HiddenByAge(role, mails))
	}
	if err := MarkRunStart(role, start); err != nil {
		t.Fatal(err)
	}
	if got := unread(); got != 2 || HiddenByAge(role, mails) != 1 {
		t.Fatalf("after MarkRunStart: unread=%d hidden=%d", got, HiddenByAge(role, mails))
	}
}

// CursorAt says when mail was last shown to a role (endFinishedRuns reads it as a resume).
func TestCursorAtMovesWhenMailIsShown(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	if _, ok := CursorAt("operator-x"); ok {
		t.Fatal("a role with no cursor has a time")
	}
	before := time.Now().Add(-time.Second)
	if err := MarkRead("operator-x", agentmail.Mail{ID: "7"}); err != nil {
		t.Fatal(err)
	}
	if at, ok := CursorAt("operator-x"); !ok || at.Before(before) {
		t.Fatalf("cursor at %v ok=%v", at, ok)
	}
}
