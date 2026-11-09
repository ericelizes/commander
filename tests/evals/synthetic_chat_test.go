package evals

import (
	"bufio"
	"bytes"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"testing"

	"github.com/example-user/commander/internal/chat"
)

func TestSyntheticChatScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/synthetic-chat.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		SharedMessage struct {
			ID       string          `json:"id"`
			Thinking string          `json:"thinking"`
			ToolID   string          `json:"tool_id"`
			Input    json.RawMessage `json:"input"`
		} `json:"shared_message"`
		Stream         string          `json:"stream"`
		Transcript     string          `json:"transcript"`
		Tools          []string        `json:"tools"`
		StreamCost     float64         `json:"stream_cost"`
		TranscriptCost float64         `json:"transcript_cost"`
		ResumeResult   json.RawMessage `json:"resume_result"`
		ResumeDelta    float64         `json:"resume_delta"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(fx.Stream)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n := chat.NewNormalizer(0, true, nil)
	var live []chat.Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		events, _ := n.Line(sc.Bytes())
		live = append(live, events...)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	history, info, err := chat.ReadTranscript(fx.Transcript, "manager-demo")
	if err != nil {
		t.Fatal(err)
	}
	if n.LastTotal() != fx.StreamCost || !info.CostKnown || info.TotalCostUSD != fx.TranscriptCost {
		t.Fatalf("costs: %v, %+v", n.LastTotal(), info)
	}
	for name, events := range map[string][]chat.Event{"live": live, "history": history} {
		var tools []string
		var denied, interrupted bool
		for _, e := range events {
			if e.Kind == chat.KindToolResult {
				tools = append(tools, e.Tool.Name)
				denied = denied || e.Tool.IsError
			}
			interrupted = interrupted || e.Kind == chat.KindNotice && e.Subtype == "interrupted" || e.Kind == chat.KindResult && e.Result.TerminalReason == "aborted_streaming"
		}
		if !reflect.DeepEqual(tools, fx.Tools) || !denied || !interrupted {
			t.Fatalf("%s: tools %v, denied %v, interrupted %v", name, tools, denied, interrupted)
		}
	}
	// Replay and hydration must attribute both blocks to the same message.
	for name, events := range map[string][]chat.Event{"live": live, "history": history} {
		var thinking, input string
		var thinkingParts, inputParts, finals int
		for _, e := range events {
			if e.MessageID != fx.SharedMessage.ID {
				continue
			}
			switch e.Kind {
			case chat.KindThinkingDelta:
				thinking += e.Text
				thinkingParts++
				if e.Block == nil || *e.Block != 0 {
					t.Fatalf("%s thinking attribution: %+v", name, e)
				}
			case chat.KindToolInputDelta:
				input += e.Tool.PartialJSON
				inputParts++
				if e.Block == nil || *e.Block != 1 {
					t.Fatalf("%s input attribution: %+v", name, e)
				}
			case chat.KindThinking:
				if e.Block == nil || *e.Block != 0 || e.Text != fx.SharedMessage.Thinking {
					t.Fatalf("%s thinking final: %+v", name, e)
				}
				finals++
			case chat.KindToolInput:
				var want, got bytes.Buffer
				if err := json.Compact(&want, fx.SharedMessage.Input); err != nil {
					t.Fatal(err)
				}
				if err := json.Compact(&got, e.Tool.Input); err != nil {
					t.Fatal(err)
				}
				if e.Block == nil || *e.Block != 1 || e.Tool.ID != fx.SharedMessage.ToolID || got.String() != want.String() {
					t.Fatalf("%s input final: %+v", name, e)
				}
				if name == "live" && (input != got.String() || thinking != fx.SharedMessage.Thinking || inputParts < 2 || thinkingParts < 2) {
					t.Fatalf("incomplete live accumulation: %q / %q (%d/%d)", thinking, input, thinkingParts, inputParts)
				}
				finals++
			}
		}
		if finals != 2 {
			t.Fatalf("%s shared-message finals = %d", name, finals)
		}
	}
	var compact bool
	for _, e := range history {
		compact = compact || e.Kind == chat.KindCompaction
	}
	if !compact {
		t.Fatal("hydrated history lost compaction")
	}
	resumed := chat.NewNormalizer(info.TotalCostUSD, info.CostKnown, nil)
	result, _ := resumed.Line(fx.ResumeResult)
	if len(result) != 1 || result[0].Kind != chat.KindResult || math.Abs(result[0].Result.CostDeltaUSD-fx.ResumeDelta) > 1e-9 {
		t.Fatalf("resumed turn: %+v", result)
	}
}
