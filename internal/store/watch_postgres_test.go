package store

import (
	"context"
	"reflect"
	"testing"
)

func TestPostgresWatchIssuesPersistFlagsAndReplaceAtomically(t *testing.T) {
	s := postgresTestStore(t)
	ctx := context.Background()
	issues := []WatchIssue{
		{Key: "quiet", WS: "work", Text: "Quiet without progress", Since: 10, Active: true, Told: false},
		{Key: "already-told", WS: "work", Text: "Already delivered", Since: 20, Active: false, Told: true},
		{Key: "inactive", WS: "other", Text: "Pending observation", Since: 30, Active: false, Told: false},
		{Key: "active-and-told", WS: "other", Text: "Delivered active alert", Since: 40, Active: true, Told: true},
	}
	if err := s.SetWatchIssues(ctx, issues); err != nil {
		t.Fatal(err)
	}
	got, err := s.WatchIssues(ctx)
	if err != nil || !reflect.DeepEqual(got, issues) {
		t.Fatal("flags did not survive canonical storage", got, err)
	}
	if err := s.SetWatchIssues(ctx, issues[1:]); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWatchIssues(ctx, []WatchIssue{issues[0], issues[0]}); err == nil {
		t.Fatal("invalid replacement unexpectedly succeeded")
	}
	got, err = s.WatchIssues(ctx)
	if err != nil || !reflect.DeepEqual(got, issues[1:]) {
		t.Fatal("failed replacement erased durable alerts", got, err)
	}
}
