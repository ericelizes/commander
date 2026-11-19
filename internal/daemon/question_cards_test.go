package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/store"
)

// zq9h.6.85.2.7 on Nov 19: its question was superseded and the card stayed on
// Eric's list. A card whose question is answered or superseded is withdrawn.
func TestStaleQuestionCardIsWithdrawn(t *testing.T) {
	stale := store.OpenQuestion{Project: "vibe-apps", Bead: "vibe-apps-zq9h.6.85.2.7"}
	var got []string
	n := withdrawQuestionCards(context.Background(), func(context.Context) ([]store.OpenQuestion, error) { return []store.OpenQuestion{stale}, nil },
		func(_ context.Context, args ...string) error { got = args; return nil })
	if n != 1 || strings.Join(got, " ") != "internal-withdraw-card --project vibe-apps --bead vibe-apps-zq9h.6.85.2.7" {
		t.Fatalf("%d %v", n, got)
	}
	if answerText("I'll tell you", "C0123 for admin alerts") != "I'll tell you: C0123 for admin alerts" {
		t.Fatal("answer text")
	}
}
