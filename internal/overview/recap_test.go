package overview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixture() Input {
	return Input{Scope: "p:w", Goal: "Ship dashboards", Evidence: []Evidence{{ID: "pr", Label: "PR", Text: "PR open; apply pending"}}}
}
func valid() Recap {
	one := func(text string) Claim { return Claim{Sentences: []Sentence{{Text: text, Sources: []string{"pr"}}}} }
	return Recap{
		ProblemType:   "feature",
		Problem:       Claim{Sentences: []Sentence{{Text: "The PR is open.", Sources: []string{"pr"}}, {Text: "The infrastructure apply is still pending.", Sources: []string{"pr"}}}},
		ProblemImpact: one("Teams lose an hour a week finding failures."),
		Solution:      one("A pull request adds the dashboards."),
		Impact:        one("Teams see their apps at a glance."),
		Scope:         "Small", ScopeDetail: one("One pull request."),
		Risk: 2, RiskDetail: one("Read-only dashboards."),
	}
}
func waitReady(t *testing.T, c *Cache, in Input) Result {
	t.Helper()
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		r := c.Get(in)
		if r.State == "ready" || r.State == "retrying" {
			return r
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("reader did not finish")
	return Result{}
}
func TestCacheOwnsOneReaderAndMarksChangedEvidenceStale(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	c := New(func(context.Context, Input) (Recap, error) { calls.Add(1); <-release; return valid(), nil })
	in := fixture()
	r := c.Get(in)
	if r.State != "updating" {
		t.Fatal(r)
	}
	for i := 0; i < 12; i++ {
		c.Get(in)
	}
	other := in
	other.Scope = "other"
	if r := c.Get(other); r.State != "waiting" {
		t.Fatal(r)
	}
	close(release)
	r = waitReady(t, c, in)
	if calls.Load() != 1 || r.Stale || r.Recap == nil {
		t.Fatal(r, calls.Load())
	}
	for i := 0; i < 12; i++ {
		c.Get(in)
	}
	if calls.Load() != 1 {
		t.Fatal("unchanged evidence reran reader")
	}
	in.Evidence[0].Text = "PR now merged"
	r = c.Get(in)
	if !r.Stale || r.Recap == nil || r.Evidence[0].Text != "PR open; apply pending" {
		t.Fatal("stale recap lost original citations", r)
	}
}
func TestRecapWaitsForTheCommanderTurnToEnd(t *testing.T) {
	var calls atomic.Int32
	c := New(func(context.Context, Input) (Recap, error) { calls.Add(1); return valid(), nil })
	in := fixture()
	in.Hold = true
	c.Get(in)
	waitReady(t, c, in) // no recap yet: the first one is written even mid-turn
	in.Evidence[0].Text = "PR now merged"
	if r := c.Get(in); r.State != "ready" || calls.Load() != 1 {
		t.Fatalf("rewrote mid-turn: %+v calls %d", r, calls.Load())
	}
}

func TestReaderFailureIsBoundedAndDoesNotInventRecap(t *testing.T) {
	var calls atomic.Int32
	c := New(func(context.Context, Input) (Recap, error) { calls.Add(1); return Recap{}, errors.New("provider down") })
	r := waitReady(t, c, fixture())
	if r.State != "retrying" || r.Recap != nil || r.Error == "" {
		t.Fatal(r)
	}
	c.Get(fixture())
	if calls.Load() != 1 {
		t.Fatal("failure retry loop")
	}
}
func TestReaderCannotCiteInventedEvidence(t *testing.T) {
	r := valid()
	r.Problem.Sentences[1].Sources = []string{"made-up"}
	if Validate(r, fixture()) == nil {
		t.Fatal("accepted invented source")
	}
	r = valid()
	r.Impact = Claim{Sentences: []Sentence{{Text: "Approve deployment", Sources: nil}}}
	if Validate(r, fixture()) == nil {
		t.Fatal("accepted uncited action")
	}
	r = valid()
	r.Problem.Sentences[0].Sources = []string{"pr", "pr", "pr", "pr"}
	if Validate(r, fixture()) == nil {
		t.Fatal("accepted a pile of sources on one sentence")
	}
}
func TestRecapEnvironmentDoesNotInheritParentRoleOrWrongAccount(t *testing.T) {
	got := cleanEnv([]string{"PATH=/bin", "CLAUDECODE=1", "CLAUDE_CONFIG_DIR=/wrong", "COMMANDER_ROLE=commander"}, []string{"CLAUDE_CONFIG_DIR=/selected"})
	if len(got) != 2 || got[0] != "PATH=/bin" || got[1] != "CLAUDE_CONFIG_DIR=/selected" {
		t.Fatal(got)
	}
}

func TestClaudeReaderHasNoToolsOrAmbientInstructions(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv")
	body, _ := json.Marshal(valid())
	envelope, _ := json.Marshal(map[string]json.RawMessage{"structured_output": body})
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$OV_CAPTURE\"\ncat >/dev/null\nprintf '%s' '" + string(envelope) + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	r, err := Claude([]string{"OV_CAPTURE=" + capture})(context.Background(), fixture())
	if err != nil {
		t.Fatal(err)
	}
	if Validate(r, fixture()) != nil {
		t.Fatal(r)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	args := string(raw)
	for _, flag := range []string{"--safe-mode\n", "--tools\n\n", "--strict-mcp-config\n", "--mcp-config\n{\"mcpServers\":{}}\n", "--no-session-persistence\n", "--json-schema\n"} {
		if !strings.Contains(args, flag) {
			t.Fatalf("missing read-only boundary %q: %s", flag, args)
		}
	}
}

func TestCacheBoundIncludesWaitingScopes(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	c := New(func(context.Context, Input) (Recap, error) { <-release; return valid(), nil })
	for i := 0; i < 200; i++ {
		in := fixture()
		in.Scope = fmt.Sprint(i)
		c.Get(in)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) > 128 {
		t.Fatalf("unbounded waiting scopes: %d", len(c.entries))
	}
}

func TestPromptRequiresEveryNamedThingToBeExplained(t *testing.T) {
	for _, want := range []string{"EXPLAIN EVERYTHING YOU NAME", "what it is and why it matters", "pull request", "the records do not say", "never write \"step 4\""} {
		if !strings.Contains(Prompt, want) {
			t.Fatalf("prompt lost %q", want)
		}
	}
	// The explanation sentences must fit the schema the reader is held to.
	if !strings.Contains(Prompt, "the REAL impact today") {
		t.Fatal("the pitch must say what the real problem is")
	}
}

func TestPromptLeadsWithOutcomesNotReviewMechanics(t *testing.T) {
	for _, want := range []string{"LEAD WITH OUTCOMES, NOT REVIEW MECHANICS", "what changed, what was decided", "adversarial reviews, review rounds, review receipts", "only when they are the thing blocking", "finished Operator task's outcome", "ran and changed nothing"} {
		if !strings.Contains(Prompt, want) {
			t.Fatalf("prompt lost %q", want)
		}
	}
	if strings.Index(Prompt, "LEAD WITH OUTCOMES") > strings.Index(Prompt, "EXPLAIN EVERYTHING") {
		t.Fatal("outcome rule must come before the explain rule")
	}
}

func TestPromptMarksCodeLikeText(t *testing.T) {
	if !strings.Contains(Prompt, "wrap code-like text") {
		t.Fatal("the recap prompt must ask for code-like text in backticks")
	}
}

func TestPitchNeedsTypeScopeAndRisk(t *testing.T) {
	in := fixture()
	r := valid()
	if err := Validate(r, in); err != nil {
		t.Fatal(err)
	}
	for _, broken := range []func(*Recap){
		func(r *Recap) { r.ProblemType = "chore" },
		func(r *Recap) { r.Scope = "Huge" },
		func(r *Recap) { r.Risk = 11 },
		func(r *Recap) { r.RiskDetail = Claim{} },
	} {
		b := valid()
		broken(&b)
		if Validate(b, in) == nil {
			t.Fatalf("accepted %+v", b)
		}
	}
}
