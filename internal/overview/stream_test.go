package overview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example-user/commander/internal/testguard"
)

const partialJSON = `{"now":{"sentences":[{"text":"The PR is \"open\".","sources":["pr"]},{"text":"Second sent`

func TestPartialRecapKeepsOnlyFinishedSentences(t *testing.T) {
	r := PartialRecap(partialJSON)
	if len(r.Now.Sentences) != 2 || r.Now.Sentences[0].Text != `The PR is "open".` || r.Now.Sentences[0].Partial {
		t.Fatal(r)
	}
	if p := r.Now.Sentences[1]; !p.Partial || p.Text != "Second sent" || len(p.Sources) != 0 {
		t.Fatal(p)
	}
	full := `{"problem_type":"bug","problem":{"sentences":[{"text":"A.","sources":["pr"]}]},"solution":{"sentences":[{"text":"B.","sources":["pr"]},{"text":"C.","sources":["pr"]}]},"impact":{"sentences":[{"text":"D.","sources":["x"]}]},"scope":"Small","risk":3,"risk_detail":{"sentences":[{"text":"E.","sources":[]}]}}`
	r = PartialRecap(full)
	if len(r.Solution.Sentences) != 2 || len(r.Impact.Sentences) != 1 || len(r.RiskDetail.Sentences) != 1 || r.RiskDetail.Sentences[0].Text != "E." || r.ProblemType != "bug" || r.Scope != "Small" || r.Risk != 3 {
		t.Fatalf("%+v", r)
	}
	if got := onlyKnown(r, fixture()); len(got.Impact.Sentences[0].Sources) != 0 || len(got.Solution.Sentences[0].Sources) != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestCacheShowsDraftWhileWritingAndSignalsChange(t *testing.T) {
	step, release := make(chan struct{}), make(chan struct{})
	c := New(func(ctx context.Context, in Input) (Recap, error) {
		progressFrom(ctx)(PartialRecap(partialJSON))
		close(step)
		<-release
		return valid(), nil
	})
	in := fixture()
	ch := c.Changed()
	c.Get(in)
	<-step
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("no change signal")
	}
	r := c.Get(in)
	if r.State != "updating" || r.Draft == nil || len(r.Draft.Now.Sentences) != 2 {
		t.Fatalf("%+v", r)
	}
	close(release)
	if r := waitReady(t, c, in); r.Draft != nil || r.Recap == nil {
		t.Fatalf("%+v", r)
	}
}

func TestFailedReaderRetriesAndNeverReadsUnavailable(t *testing.T) {
	c := New(func(context.Context, Input) (Recap, error) { return Recap{}, os.ErrNotExist })
	r := waitReady(t, c, fixture())
	if r.State != "retrying" || r.WaitingFor == "" || r.Error == "" {
		t.Fatalf("%+v", r)
	}
}

func TestClaudeStreamsDraftThenResult(t *testing.T) {
	dir := t.TempDir()
	testguard.NoLeakedProcesses(t, dir)
	script := `#!/bin/sh
cat >/dev/null
echo '{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use"}}}'
echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"problem\":{\"sentences\":[{\"text\":\"Open.\",\"sources\":[\"pr\"]}"}}}'
echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"]}}"}}}'
echo '{"type":"result","is_error":false,"structured_output":{"problem":{"sentences":[{"text":"Open.","sources":["pr"]}]}}}'
`
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	var drafts []Recap
	ctx := WithProgress(context.Background(), func(d Recap) { drafts = append(drafts, d) })
	r, err := Claude(nil)(ctx, fixture())
	if err != nil || len(r.Problem.Sentences) != 1 {
		t.Fatal(r, err)
	}
	if len(drafts) != 2 || len(drafts[0].Problem.Sentences) != 1 {
		t.Fatalf("%+v", drafts)
	}
}

func TestChangedEvidenceWaitsOutTheCooldownAndDraftCarriesItsOwnEvidence(t *testing.T) {
	var calls atomic.Int32
	step, release := make(chan struct{}), make(chan struct{})
	c := New(func(ctx context.Context, in Input) (Recap, error) {
		if calls.Add(1) == 2 {
			progressFrom(ctx)(PartialRecap(partialJSON))
			close(step)
			<-release
		}
		return valid(), nil
	})
	in := fixture()
	c.Get(in)
	waitReady(t, c, in)
	in.Evidence = []Evidence{{ID: "pr", Label: "PR", Text: "PR now merged"}}
	for i := 0; i < 5; i++ {
		if r := c.Get(in); r.State != "ready" || !r.Stale {
			t.Fatalf("changed evidence inside the cooldown: %+v", r)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("reader reran inside the cooldown")
	}
	c.mu.Lock()
	c.entries[in.Scope].attempt = time.Now().Add(-changeAfter)
	c.mu.Unlock()
	c.Get(in)
	<-step
	r := c.Get(in)
	if r.State != "updating" || r.Draft == nil || r.Evidence[0].Text != "PR now merged" {
		t.Fatalf("draft must carry its own evidence: %+v", r)
	}
	close(release)
	waitReady(t, c, in)
}

func TestFirstDeltaPublishesPartialDraftAndDraftsAreNonNil(t *testing.T) {
	in := Input{Evidence: []Evidence{{ID: "a"}}}
	// The first sentence shows as it is written, marked partial.
	if p := PartialRecap(`{"problem":{"sentences":[{"text":"Half wri`); len(p.Problem.Sentences) != 1 || !p.Problem.Sentences[0].Partial {
		t.Fatalf("first partial sentence: %+v", p)
	}
	if p := PartialRecap(`{"problem":{"sentences":[{"te`); len(p.Problem.Sentences) != 0 {
		t.Fatalf("no text yet: %+v", p)
	}
	// A sentence citing only unknown ids still encodes arrays, never null.
	d := onlyKnown(PartialRecap(`{"problem":{"sentences":[{"text":"x","sources":["zz"]}]}}`), in)
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "null") {
		t.Fatalf("draft encodes null: %s", b)
	}
}

// Every prefix of the answer yields text that only grows, one character at a time.
func TestPartialRecapStreamsCharacterByCharacter(t *testing.T) {
	full := `{"problem":{"sentences":[{"text":"Caf\u00e9 \"ok\" done.","sources":["pr"]}]},"solution":{"sentences":[{"text":"Next one","sources":[]}]}}`
	prev := ""
	grew := 0
	for i := 1; i <= len(full); i++ {
		r := PartialRecap(full[:i])
		cur := ""
		if n := len(r.Problem.Sentences); n > 0 {
			cur = r.Problem.Sentences[0].Text
		}
		if !strings.HasPrefix(cur, prev) || len([]rune(cur))-len([]rune(prev)) > 1 {
			t.Fatalf("at %d: %q after %q", i, cur, prev)
		}
		if cur != prev {
			grew++
		}
		prev = cur
	}
	if prev != `Café "ok" done.` || grew != len([]rune(prev)) {
		t.Fatalf("%q grew %d", prev, grew)
	}
	r := PartialRecap(full[:strings.Index(full, `"Next one"`)+5])
	if len(r.Solution.Sentences) != 1 || r.Solution.Sentences[0].Text != "Next" || !r.Solution.Sentences[0].Partial {
		t.Fatalf("%+v", r.Solution)
	}
}

// An escaped emoji arrives as two \u escapes; until the low half lands the
// draft holds the high half back instead of showing U+FFFD. Raw UTF-8 cut
// mid-character is held back the same way.
func TestPartialRecapHoldsAHighSurrogateBack(t *testing.T) {
	bs := `\`
	emoji := bs + "u" + "d83d" + bs + "u" + "de00"
	for _, text := range []string{"Hi " + emoji + " there", "Hi " + string(rune(0x1F600)) + " there"} {
		full := `{"problem":{"sentences":[{"text":"` + text + `","sources":[]}]}}`
		for i := 1; i <= len(full); i++ {
			r := PartialRecap(full[:i])
			if n := len(r.Problem.Sentences); n > 0 && strings.ContainsRune(r.Problem.Sentences[0].Text, 0xFFFD) {
				t.Fatalf("at %d: %q", i, r.Problem.Sentences[0].Text)
			}
		}
		if got := PartialRecap(full).Problem.Sentences[0].Text; got != "Hi "+string(rune(0x1F600))+" there" {
			t.Fatalf("%q", got)
		}
	}
	// An escaped backslash before u is no escape: the text stays as written.
	if got := openText(`{"text":"a` + bs + bs + "u" + "d83d"); got != "a"+bs+"u"+"d83d" {
		t.Fatalf("%q", got)
	}
}
