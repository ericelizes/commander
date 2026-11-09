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

const partialJSON = `{"summary":{"sentences":[{"text":"The PR is \"open\".","sources":["pr"]},{"text":"Second sent`

func TestPartialRecapKeepsOnlyFinishedSentences(t *testing.T) {
	r := PartialRecap(partialJSON)
	if len(r.Summary.Sentences) != 1 || r.Summary.Sentences[0].Text != `The PR is "open".` {
		t.Fatal(r)
	}
	full := `{"summary":{"sentences":[{"text":"A.","sources":["pr"]}]},"changes":[{"sentences":[{"text":"B.","sources":["pr"]},{"text":"C.","sources":["pr"]}]},{"sentences":[{"text":"D.","sources":["x"]}]}],"next":[{"sentences":[{"text":"E.","sources":[]}]}]}`
	r = PartialRecap(full)
	if len(r.Changes) != 2 || len(r.Changes[0].Sentences) != 2 || len(r.Changes[1].Sentences) != 1 || len(r.Next) != 1 || r.Next[0].Sentences[0].Text != "E." {
		t.Fatalf("%+v", r)
	}
	if got := onlyKnown(r, fixture()); len(got.Changes[1].Sentences[0].Sources) != 0 || len(got.Changes[0].Sentences[0].Sources) != 1 {
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
	if r.State != "updating" || r.Draft == nil || len(r.Draft.Summary.Sentences) != 1 {
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
echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"summary\":{\"sentences\":[{\"text\":\"Open.\",\"sources\":[\"pr\"]}"}}}'
echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"]},\"changes\":[],\"next\":[]}"}}}'
echo '{"type":"result","is_error":false,"structured_output":{"summary":{"sentences":[{"text":"Open.","sources":["pr"]}]},"changes":[],"next":[]}}'
`
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	var drafts []Recap
	ctx := WithProgress(context.Background(), func(d Recap) { drafts = append(drafts, d) })
	r, err := Claude(nil)(ctx, fixture())
	if err != nil || len(r.Summary.Sentences) != 1 {
		t.Fatal(r, err)
	}
	if len(drafts) != 2 || len(drafts[0].Summary.Sentences) != 1 {
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

func TestFirstDeltaPublishesNoDraftAndDraftsAreNonNil(t *testing.T) {
	in := Input{Evidence: []Evidence{{ID: "a"}}}
	// Before any sentence finishes the partial recap holds nothing to show.
	if p := PartialRecap(`{"summary":{"sentences":[{"text":"Half wri`); len(p.Summary.Sentences) != 0 {
		t.Fatalf("draft before a finished sentence: %+v", p)
	}
	// A sentence citing only unknown ids still encodes arrays, never null.
	d := onlyKnown(PartialRecap(`{"summary":{"sentences":[{"text":"x","sources":["zz"]}]},"changes":[],"next":[]}`), in)
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "null") {
		t.Fatalf("draft encodes null: %s", b)
	}
}
