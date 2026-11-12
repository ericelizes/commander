package statusclassify

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDescriberWritesStateSpecificLinesAndNeverShowsAStaleState(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	var prompts []string
	fail := false
	words := "approve the refund guard so posting stops misrouting refunds"
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
	if !ok || text != "Needs you: "+words || strings.Count(text, "Needs you:") != 1 {
		t.Fatalf("written line %q", text)
	}
	if !strings.Contains(prompts[0], "say exactly what they must decide") {
		t.Fatal("the user prompt was not chosen by the state")
	}
	// Same state, new facts: the old line stays while the new one is written.
	if text, _, ok := d.Describe("p:ws", "user", "decision", "Needs you:", "facts v2"); !ok || text != "Needs you: "+words {
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
	if text, _, ok := d.Describe("p:ws", "user", "decision", "Needs you:", "facts v1"); !ok || text != "Needs you: "+words {
		t.Fatal("a failed call erased the last good line")
	}
}

func TestShortenDropsATrailingClauseBeforeCutting(t *testing.T) {
	if got := shorten("decide whether to stop staging or all of Notify, unblocking the rollout plan", 60); got != "decide whether to stop staging or all of Notify" {
		t.Fatalf("%q", got)
	}
}

func TestFinishDescriptionKeepsOneOpeningAndRejectsJunk(t *testing.T) {
	if got, ok := finishDescription("Working:", "Working: two agents run the parity tests"); !ok || got != "Working: two agents run the parity tests" {
		t.Fatalf("%q", got)
	}
	if _, ok := finishDescription("Working:", "   "); ok {
		t.Fatal("empty accepted")
	}
	long := strings.Repeat("parity tests ", 12)
	if got, ok := finishDescription("Working:", long); !ok || len([]rune(got)) > len("Working: ")+maxDescriptionWords+1 || !strings.HasSuffix(got, "…") {
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
