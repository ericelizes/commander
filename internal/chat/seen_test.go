package chat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func seenHome(t *testing.T, log []byte) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "chat"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "chat", "commander.jsonl"), log, 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestSeenDigestMatchesGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/seen.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/seen.golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	got := SeenDigest(seenHome(t, raw), "commander", SeenExchanges)
	if got != strings.TrimRight(string(want), "\n") {
		t.Fatalf("digest differs from testdata/seen.golden.txt:\n%s", got)
	}
	for _, hidden := range []string{"SECRET TOOL OUTPUT", "nobody saw", "Let me check", "board v12", "Stop hook feedback", "Request interrupted", "Checking the docs row first"} {
		if strings.Contains(got, hidden) {
			t.Errorf("digest shows %q, which the user was never shown as a message or final reply", hidden)
		}
	}
}

func TestSeenDigestKeepsTheLastExchangesNewestLast(t *testing.T) {
	raw, _ := os.ReadFile("testdata/seen.jsonl")
	got := SeenDigest(seenHome(t, raw), "commander", 2)
	if !strings.Contains(got, "the last 2 exchanges") || !strings.Contains(got, "Next?") || strings.Contains(got, "Stop.") {
		t.Fatalf("got %q", got)
	}
	if strings.Index(got, "Next?") > strings.Index(got, "Are you there?") {
		t.Fatalf("newest exchange is not last: %q", got)
	}
}

func TestSeenDigestTrimsLongMessages(t *testing.T) {
	at := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	lines := []Event{
		{Seq: 1, Kind: KindUser, Origin: "user", Source: SourceEngine, Time: at, Text: strings.Repeat("u", 5000)},
		{Seq: 2, Kind: KindResult, Source: SourceLive, Time: at, Result: &Result{Text: strings.Repeat("r", 5000)}},
	}
	got := SeenDigest(seenHome(t, jsonl(t, lines)), "commander", SeenExchanges)
	if !strings.Contains(got, "User: "+strings.Repeat("u", seenUserRunes-1)+"… (trimmed)") ||
		!strings.Contains(got, "You replied: "+strings.Repeat("r", seenReplyRunes-1)+"… (trimmed)") ||
		strings.Contains(got, strings.Repeat("r", seenReplyRunes)) {
		t.Fatalf("not trimmed: %d bytes", len(got))
	}
}

// A long log is read from its end; the oldest exchange of the slice may be
// cut off, so it is dropped rather than shown without its message.
func TestSeenDigestReadsALongLogFromItsEnd(t *testing.T) {
	at := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	pad := strings.Repeat("x", 4000)
	var lines []Event
	for i := 0; i < 1200; i++ {
		seq := uint64(i * 3)
		lines = append(lines,
			Event{Seq: seq + 1, Kind: KindUser, Origin: "user", Source: SourceEngine, Time: at, Text: fmt.Sprintf("question %d", i)},
			Event{Seq: seq + 2, Kind: KindToolResult, Source: SourceLive, Time: at, Tool: &Tool{ID: "t", Content: pad}},
			Event{Seq: seq + 3, Kind: KindResult, Source: SourceLive, Time: at, Result: &Result{Text: fmt.Sprintf("answer %d", i)}})
	}
	raw := jsonl(t, lines)
	if len(raw) <= seenTailStart {
		t.Fatalf("log of %d bytes does not exercise the tail read", len(raw))
	}
	got := SeenDigest(seenHome(t, raw), "commander", 3)
	want := "User: question 1197\nYou replied: answer 1197"
	if !strings.Contains(got, want) || !strings.HasSuffix(got, "User: question 1199\nYou replied: answer 1199") || strings.Contains(got, "1196") {
		t.Fatalf("got %q", got)
	}
}

func TestSeenDigestEmptyWithoutALog(t *testing.T) {
	if got := SeenDigest(t.TempDir(), "commander", SeenExchanges); got != "" {
		t.Fatalf("got %q", got)
	}
}

func jsonl(t *testing.T, events []Event) []byte {
	t.Helper()
	var b strings.Builder
	for _, e := range events {
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}
