package statusclassify

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDescriberWritesStateSpecificLinesAndNeverShowsAStaleState(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	var prompts []string
	fail := false
	words := "approve the refund guard?"
	d := &Describer{Now: func() time.Time { return clock }, Call: func(_ context.Context, _, instruction, input string, out any) error {
		prompts = append(prompts, instruction)
		if fail {
			return errors.New("no account")
		}
		raw, _ := json.Marshal(map[string]string{"description": "Needs you: " + words, "signal": "none"})
		return json.Unmarshal(raw, out)
	}}
	d.init()
	if _, _, ok := d.Describe("p:ws", "user", "decision", "Needs you:", "facts v1"); ok {
		t.Fatal("a description before any was written")
	}
	job, ok := d.next()
	if !ok || !d.write(context.Background(), job) {
		t.Fatal("pending description was not written")
	}
	text, _, ok := d.Describe("p:ws", "user", "decision", "Needs you:", "facts v1")
	if !ok || text != "Approve the refund guard?" || strings.Contains(text, "Needs you") {
		t.Fatalf("written line %q", text)
	}
	if !strings.Contains(prompts[0], "It is the reader's move") {
		t.Fatal("the user prompt was not chosen by the state")
	}
	// Same state, new facts: the old line stays while the new one is written.
	if text, _, ok := d.Describe("p:ws", "user", "decision", "Needs you:", "facts v2"); !ok || text != "Approve the refund guard?" {
		t.Fatal("a same-state line was dropped while its refresh is pending")
	}
	// New state: never the old line.
	if _, _, ok := d.Describe("p:ws", "agent", "executing", "Working:", "facts v3"); ok {
		t.Fatal("a line written for another state was shown")
	}
	// A new state is described at once; the same state with new facts waits.
	if job, ok := d.next(); !ok || job.actor != "agent" {
		t.Fatal("a state change was not described at once")
	}
	d.Describe("p:ws", "user", "decision", "Needs you:", "facts v2")
	if _, ok := d.next(); ok {
		t.Fatal("a same-state refresh skipped the 30-minute floor")
	}
	clock = clock.Add(31 * time.Minute)
	fail = true
	job, _ = d.next()
	if d.write(context.Background(), job) {
		t.Fatal("a failed call reported a description")
	}
	if text, _, ok := d.Describe("p:ws", "user", "decision", "Needs you:", "facts v1"); !ok || text != "Approve the refund guard?" {
		t.Fatal("a failed call erased the last good line")
	}
}

func TestTooLongAnswerIsRewrittenOnceBeforeCutting(t *testing.T) {
	calls := 0
	d := &Describer{Call: func(_ context.Context, _, _, input string, out any) error {
		calls++
		text := "decide whether to merge and install the reviewed Commander repair batch into main today"
		if strings.Contains(input, "Rewrite this status") {
			text = "merge the repair batch?"
		}
		raw, _ := json.Marshal(map[string]string{"description": text, "signal": "none"})
		return json.Unmarshal(raw, out)
	}}
	d.init()
	d.Describe("p:ws", "user", "decision", "Needs you:", "facts")
	job, _ := d.next()
	if !d.write(context.Background(), job) || calls != 2 {
		t.Fatalf("calls %d", calls)
	}
	if text, _, _ := d.Describe("p:ws", "user", "decision", "Needs you:", "facts"); text != "Merge the repair batch?" || strings.Contains(text, "…") {
		t.Fatalf("line %q", text)
	}
}

func TestPromptNamesTheReaderAsYou(t *testing.T) {
	if got := (&Describer{UserName: "Eric Elizes"}).prompt("none"); !strings.Contains(got, "write 'you' wherever the facts say Eric Elizes") {
		t.Fatalf("%q", got)
	}
	if got := (&Describer{}).prompt("none"); got != promptFor("none") {
		t.Fatal("no name, no extra sentence")
	}
}

func TestWrittenLinesSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status-lines.json")
	answer := func(_ context.Context, _, _, _ string, out any) error {
		raw, _ := json.Marshal(map[string]string{"description": "merge the dashboard?", "signal": "none"})
		return json.Unmarshal(raw, out)
	}
	d := &Describer{CachePath: path, Call: answer}
	d.Describe("p:ws", "user", "decision", "Needs you:", "facts v1")
	job, _ := d.next()
	if !d.write(context.Background(), job) {
		t.Fatal("not written")
	}
	restarted := &Describer{CachePath: path, Call: answer}
	if text, _, ok := restarted.Describe("p:ws", "user", "decision", "Needs you:", "facts v2"); !ok || text != "Merge the dashboard?" {
		t.Fatalf("after a restart: %q %v", text, ok)
	}
	if _, _, ok := restarted.Describe("p:ws", "agent", "executing", "Working:", "facts v3"); ok {
		t.Fatal("a kept line was shown for another state")
	}
}

func TestSameColorKeepsTheOldLineUntilTheNewOne(t *testing.T) {
	d := &Describer{Call: func(_ context.Context, _, _, _ string, out any) error {
		raw, _ := json.Marshal(map[string]string{"description": "agent rebuilding the release", "signal": "none"})
		return json.Unmarshal(raw, out)
	}}
	d.Describe("p:ws", "agent", "executing", "Working:", "facts v1")
	job, _ := d.next()
	d.write(context.Background(), job)
	if text, _, ok := d.Describe("p:ws", "agent", "blocked:executing", "Working:", "facts v2"); !ok || text != "Agent rebuilding the release" {
		t.Fatalf("same color lost its line: %q %v", text, ok)
	}
	if _, _, ok := d.Describe("p:ws", "user", "decision", "Needs you:", "facts v3"); ok {
		t.Fatal("another color showed the old line")
	}
}

func TestOneRowIsNeverWrittenTwiceAtOnce(t *testing.T) {
	d := &Describer{}
	d.init()
	d.Describe("p:ws", "agent", "executing", "Working:", "facts v1")
	if _, ok := d.next(); !ok {
		t.Fatal("no job")
	}
	d.Describe("p:ws", "agent", "executing", "Working:", "facts v2")
	if _, ok := d.next(); ok {
		t.Fatal("the same row was handed out while it is being written")
	}
}

func TestCommanderLinesNeverClaimTheReadersMove(t *testing.T) {
	if !strings.Contains(promptFor("commander"), "never write that the reader is needed") {
		t.Fatal("commander prompt")
	}
}

func TestShortenDropsATrailingClauseBeforeCutting(t *testing.T) {
	if got := shorten("stop staging or all Notify, unblocking the rollout plan", 44); got != "stop staging or all Notify" {
		t.Fatalf("%q", got)
	}
}

func TestFinishDescriptionDropsLabelsAndRejectsJunk(t *testing.T) {
	cases := map[string]string{
		"Working: QA Hub parity tests":            "QA Hub parity tests",
		"Needs you: merge the dashboard?":         "Merge the dashboard?",
		"Waiting on CI #3592: unit tests":         "Unit tests",
		"agent resolving the Navigator conflict.": "Agent resolving the Navigator conflict",
		"Commander reviewing the SIGINT fix":      "Commander reviewing the SIGINT fix",
		"“QA Hub parity tests”":                   "QA Hub parity tests",
	}
	for in, want := range cases {
		if got, ok := finishDescription("Working:", in); !ok || got != want {
			t.Fatalf("%q -> %q, want %q", in, got, want)
		}
	}
	if _, ok := finishDescription("Working:", "   "); ok {
		t.Fatal("empty accepted")
	}
	long := strings.Repeat("parity tests ", 12)
	if got, ok := finishDescription("Working:", long); !ok || len([]rune(got)) > maxDescriptionWords+1 || !strings.HasSuffix(got, "…") {
		t.Fatalf("an overlong answer was not shortened: %q", got)
	}
	for _, actor := range []string{"user", "stuck", "agent", "commander", "external", "none"} {
		if !strings.HasPrefix(promptFor(actor), describeBase) {
			t.Fatal("every prompt keeps the shared rules")
		}
	}
	if promptFor("user") == promptFor("agent") {
		t.Fatal("prompts do not differ by state")
	}
}
