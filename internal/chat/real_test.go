package chat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentrt"
)

// TestRealClaude drives the real `claude` CLI (costs plan usage: a handful of
// tiny haiku turns). Opt in with COMMANDER_CHAT_REAL=1; COMMANDER_CHAT_REAL_DIR
// is the scratch cwd and COMMANDER_CHAT_REAL_OUT receives the event log.
// It only ever uses a freshly minted throwaway session id.
func TestRealClaude(t *testing.T) {
	if os.Getenv("COMMANDER_CHAT_REAL") != "1" {
		t.Skip("set COMMANDER_CHAT_REAL=1 to run against the real claude CLI")
	}
	work := os.Getenv("COMMANDER_CHAT_REAL_DIR")
	if work == "" {
		work = t.TempDir()
	}
	out, _ := os.Create(os.Getenv("COMMANDER_CHAT_REAL_OUT"))
	if out != nil {
		defer out.Close()
	}
	id := agentrt.NewClaudeSessionID()
	t.Logf("throwaway session %s in %s", id, work)
	hook := `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"echo '{\"hookSpecificOutput\":{\"hookEventName\":\"UserPromptSubmit\",\"additionalContext\":\"MAIL from commander: the code word is KIWI-7.\"}}'"}]}]}}`
	resolver := ResolverFunc(func(_ context.Context, role string) (Spec, error) {
		return Spec{Role: role, Agent: role, SessionID: id, Cwd: work, Model: "haiku", Settings: hook}, nil
	})
	role := "operator-chatreal"
	ctx := context.Background()
	record := func(label string, events []Event) {
		for _, e := range events {
			if e.Kind == KindTextDelta || e.Kind == KindThinkingDelta || e.Kind == KindToolInputDelta {
				continue
			}
			raw, _ := json.Marshal(e)
			if out != nil {
				out.WriteString(label + " " + string(raw) + "\n")
			}
		}
	}
	waitKind := func(sub *Subscription, label string, k Kind, d time.Duration) []Event {
		t.Helper()
		var seen []Event
		timeout := time.After(d)
		for {
			select {
			case e := <-sub.C:
				seen = append(seen, e)
				if e.Kind == k {
					record(label, seen)
					return seen
				}
			case <-timeout:
				record(label, seen)
				t.Fatalf("%s: no %s within %s; saw %s", label, k, d, summarize(seen))
			}
		}
	}

	// Run 1: a turn with a Bash permission prompt, then an interrupted turn.
	e1 := New(Options{Home: t.TempDir(), Resolver: resolver, Logf: t.Logf})
	st, err := e1.Start(ctx, role, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("run1 status: running=%v pid=%d resumed=%v cli=%s", st.Running, st.PID, st.Resumed, st.CLIVersion)
	_, sub, _ := e1.Subscribe(role, 0)
	_ = e1.Send(ctx, role, "Run exactly `touch made-by-chat.txt && echo hi` with the Bash tool, then reply with only its output and the code word from your mail.")
	seen := waitKind(sub, "run1-perm", KindPermission, 90*time.Second)
	req := seen[len(seen)-1].Request
	t.Logf("permission: tool=%s input=%s suggestions=%s", req.ToolName, req.Input, req.Suggestions)
	if err := e1.Approve(ctx, role, req.ID, Decision{Behavior: "allow"}); err != nil {
		t.Fatal(err)
	}
	seen = waitKind(sub, "run1-turn", KindResult, 90*time.Second)
	t.Logf("run1 turn: %s", summarize(seen))

	_ = e1.Send(ctx, role, "Count from 1 to 300, one number per line, no other text.")
	n := 0
	timeout := time.After(90 * time.Second)
	for n < 10 {
		select {
		case e := <-sub.C:
			if e.Kind == KindTextDelta {
				n++
			}
		case <-timeout:
			t.Fatal("no deltas")
		}
	}
	t0 := time.Now()
	if err := e1.Interrupt(ctx, role); err != nil {
		t.Fatal(err)
	}
	seen = waitKind(sub, "run1-interrupt", KindResult, 30*time.Second)
	r := seen[len(seen)-1].Result
	t.Logf("interrupt: result %s/%s after %s; still running=%v", r.Subtype, r.TerminalReason, time.Since(t0), e1.Running(role))
	if err := e1.Stop(ctx, role); err != nil {
		t.Fatal(err)
	}
	sub.Close()

	// Run 2: a fresh engine (new home, empty log) resumes the session: history
	// is hydrated from Claude's transcript, the conversation continues.
	e2 := New(Options{Home: t.TempDir(), Resolver: resolver, Logf: t.Logf})
	st, err = e2.Start(ctx, role, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hist, sub2, _ := e2.Subscribe(role, 0)
	defer sub2.Close()
	record("run2-hydrated", hist)
	hydrated := 0
	for _, e := range hist {
		if e.Source == SourceTranscript {
			hydrated++
		}
	}
	t.Logf("run2: resumed=%v hydrated=%d events; base cost %.5f", st.Resumed, hydrated, st.TotalCostUSD)
	_ = e2.Send(ctx, role, "Without using tools: what file did you create, what did the command print, and what is the code word? One line.")
	seen = waitKind(sub2, "run2-turn", KindResult, 90*time.Second)
	t.Logf("run2 turn: %s", summarize(seen))

	// AskUserQuestion through can_use_tool (unverified in the spike).
	_ = e2.Send(ctx, role, "Use the AskUserQuestion tool to ask me whether I prefer red or blue (two options). After I answer, reply with only my answer.")
	deadline := time.After(90 * time.Second)
	var asked bool
loop:
	for {
		select {
		case e := <-sub2.C:
			seen = append(seen, e)
			switch e.Kind {
			case KindQuestion:
				asked = true
				t.Logf("question: %s", e.Request.Input)
				var in struct {
					Questions []struct{ Question string } `json:"questions"`
				}
				_ = json.Unmarshal(e.Request.Input, &in)
				answers := map[string]string{}
				for _, q := range in.Questions {
					answers[q.Question] = "blue"
				}
				if err := e2.Answer(ctx, role, e.Request.ID, answers); err != nil {
					t.Fatal(err)
				}
			case KindResult:
				break loop
			}
		case <-deadline:
			t.Fatal("ask turn timed out")
		}
	}
	record("run2-ask", seen)
	t.Logf("ask turn (asked=%v): %s", asked, summarize(kinds(seen, KindText)))
	if err := e2.Stop(ctx, role); err != nil {
		t.Fatal(err)
	}
	if p := FindTranscript(ClaudeProjectsDir(), id); p != "" {
		t.Logf("transcript: %s", filepath.Base(p))
	}
}
