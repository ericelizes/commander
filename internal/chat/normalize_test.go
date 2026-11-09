package chat

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
)

// normalizeFile runs an invented protocol stream through a Normalizer.
func normalizeFile(t *testing.T, path string) ([]Event, []*ControlRequest) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n := NewNormalizer(0, true, nil)
	var events []Event
	var reqs []*ControlRequest
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		evs, cr := n.Line(sc.Bytes())
		events = append(events, evs...)
		if cr != nil {
			reqs = append(reqs, cr)
		}
	}
	return events, reqs
}

func kinds(events []Event, k Kind) []Event {
	var out []Event
	for _, e := range events {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

func TestNormalizeSyntheticStream(t *testing.T) {
	events, reqs := normalizeFile(t, "testdata/stream_synthetic.jsonl")

	if s := kinds(events, KindSession); len(s) != 1 || s[0].Session.CLIVersion != "2.1.305" || s[0].Session.Model != "claude-haiku-5-5" ||
		!contains(s[0].Session.Capabilities, "interrupt_receipt_v1") {
		t.Fatalf("session events = %+v", s)
	}

	perms := kinds(events, KindPermission)
	if len(perms) != 3 || len(reqs) != 3 {
		t.Fatalf("want 3 permission requests (Write, Bash, Bash), got %d events %d reqs", len(perms), len(reqs))
	}
	if perms[0].Request.ToolName != "Write" || perms[1].Request.ToolName != "Bash" || perms[0].Request.ToolUseID == "" ||
		!strings.Contains(string(perms[1].Request.Suggestions), "addRules") || perms[1].Request.BlockedPath == "" {
		t.Fatalf("permission requests = %+v / %+v", perms[0].Request, perms[1].Request)
	}

	results := kinds(events, KindToolResult)
	if len(results) != 3 {
		t.Fatalf("tool results = %d", len(results))
	}
	if !results[0].Tool.IsError || results[0].Tool.Name != "Write" || !strings.Contains(string(results[0].Tool.Permission), "reject") {
		t.Fatalf("denied Write result = %+v", results[0].Tool)
	}
	var bash struct{ Stdout string }
	_ = json.Unmarshal(results[1].Tool.Structured, &bash)
	if results[1].Tool.Name != "Bash" || bash.Stdout != "/tmp/work" || !strings.Contains(string(results[1].Tool.Permission), "accept") {
		t.Fatalf("bash result = %+v", results[1].Tool)
	}

	// Tool calls: start (from the partial stream), then final input.
	starts, inputs := kinds(events, KindToolStart), kinds(events, KindToolInput)
	if len(starts) != 3 || len(inputs) != 3 || inputs[1].Tool.ID != starts[1].Tool.ID || !strings.Contains(string(inputs[1].Tool.Input), "made-by-bash") {
		t.Fatalf("tool starts/inputs = %d/%d %+v", len(starts), len(inputs), inputs)
	}

	// Text: deltas then a final per block, with the same message id and block.
	finals := kinds(events, KindText)
	if len(finals) < 3 {
		t.Fatalf("text finals = %d", len(finals))
	}
	for _, fin := range finals {
		var joined string
		for _, d := range kinds(events, KindTextDelta) {
			if d.MessageID == fin.MessageID && *d.Block == *fin.Block {
				joined += d.Text
			}
		}

		if joined != "" && joined != fin.Text {
			t.Fatalf("deltas of %s/%d = %q, final = %q", fin.MessageID, *fin.Block, joined, fin.Text)
		}
	}

	// Turn results: cost deltas from the cumulative totals.
	res := kinds(events, KindResult)
	if len(res) != 4 {
		t.Fatalf("results = %d", len(res))
	}
	want := []float64{0.01, 0.01, 0, 0.01}
	for i, r := range res {
		if math.Abs(r.Result.CostDeltaUSD-want[i]) > 1e-8 {
			t.Errorf("result %d delta = %v, want %v", i, r.Result.CostDeltaUSD, want[i])
		}
	}
	if res[2].Result.Subtype != "error_during_execution" || res[2].Result.TerminalReason != "aborted_streaming" {
		t.Fatalf("interrupted result = %+v", res[2].Result)
	}
	if len(kinds(events, KindRateLimit)) == 0 || len(kinds(events, KindStatus)) == 0 {
		t.Fatal("want rate_limit and status events")
	}
	for _, e := range events {
		if e.Kind == KindNotice && strings.HasPrefix(e.Subtype, "unknown:") {
			t.Fatalf("unknown event in a known stream: %s", e.Raw)
		}
	}
}

func TestNormalizeUnknownBaselineCountsOnce(t *testing.T) {
	n := NewNormalizer(0, false, nil)
	evs, _ := n.Line([]byte(`{"type":"result","subtype":"success","total_cost_usd":0.5,"session_id":"s"}`))
	if evs[0].Result.CostDeltaUSD != 0.5 {
		t.Fatalf("first delta = %v", evs[0].Result.CostDeltaUSD)
	}
	evs, _ = n.Line([]byte(`{"type":"result","subtype":"success","total_cost_usd":0.75,"session_id":"s"}`))
	if evs[0].Result.CostDeltaUSD != 0.25 {
		t.Fatalf("second delta = %v", evs[0].Result.CostDeltaUSD)
	}
}

func TestNormalizeQuestionPlanTaskCompaction(t *testing.T) {
	n := NewNormalizer(0, true, nil)
	lines := []string{
		`{"type":"control_request","request_id":"q1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Which?"}]}}}`,
		`{"type":"control_request","request_id":"p1","request":{"subtype":"can_use_tool","tool_name":"ExitPlanMode","input":{"plan":"do x"}}}`,
		`{"type":"system","subtype":"task_started","task_id":"t1","tool_use_id":"toolu_1","description":"explore"}`,
		`{"type":"assistant","parent_tool_use_id":"toolu_1","message":{"id":"m1","content":[{"type":"text","text":"nested"}]}}`,
		`{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"auto","pre_tokens":9,"post_tokens":3}}`,
		`{"type":"control_request","request_id":"h1","request":{"subtype":"hook_callback"}}`,
		`{"type":"brand_new_thing","x":1}`,
	}
	var all []Event
	var crs []*ControlRequest
	for _, l := range lines {
		evs, cr := n.Line([]byte(l))
		all = append(all, evs...)
		if cr != nil {
			crs = append(crs, cr)
		}
	}
	if len(kinds(all, KindQuestion)) != 1 || len(kinds(all, KindPlan)) != 1 {
		t.Fatalf("question/plan = %+v", all)
	}
	if tk := kinds(all, KindTask); len(tk) != 1 || tk[0].Task.TaskID != "t1" || tk[0].Subtype != "task_started" {
		t.Fatalf("task = %+v", tk)
	}
	if tx := kinds(all, KindText); len(tx) != 1 || tx[0].ParentToolUseID != "toolu_1" {
		t.Fatalf("nested text = %+v", tx)
	}
	if c := kinds(all, KindCompaction); len(c) != 1 || c[0].Compaction.PreTokens != 9 {
		t.Fatalf("compaction = %+v", c)
	}
	if len(crs) != 3 || crs[2].Subtype != "hook_callback" {
		t.Fatalf("control requests = %+v", crs)
	}
	if u := kinds(all, KindNotice); len(u) != 1 || u[0].Subtype != "unknown:brand_new_thing" || len(u[0].Raw) == 0 {
		t.Fatalf("unknown = %+v", u)
	}
}

func TestToolResultTruncated(t *testing.T) {
	big := strings.Repeat("x", maxContent+10)
	raw, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": "t", "content": big}}}})
	evs, _ := NewNormalizer(0, true, nil).Line(raw)
	if len(evs) != 1 || !evs[0].Tool.Truncated || len(evs[0].Tool.Content) > maxContent+4 {
		t.Fatalf("not truncated: %d", len(evs[0].Tool.Content))
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Observe the fixture before snapshots can hide dropped or misattributed deltas.
func TestSyntheticStreamIntermediateBlocks(t *testing.T) {
	f, err := os.Open("testdata/stream_synthetic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n := NewNormalizer(0, true, nil)
	var thinking, input string
	var thinkingParts, inputParts, stops int
	var thinkingFinal, inputFinal bool
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var raw rawLine
		if err := json.Unmarshal(sc.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		events, _ := n.Line(sc.Bytes())
		for _, e := range events {
			if e.MessageID != "message-tool-1" {
				continue
			}
			switch e.Kind {
			case KindThinkingDelta:
				if thinkingFinal || e.Block == nil || *e.Block != 0 {
					t.Fatalf("thinking attribution: %+v", e)
				}
				thinking += e.Text
				thinkingParts++
				if thinkingParts == 1 && thinking != "Check the samp" {
					t.Fatalf("partial thinking = %q", thinking)
				}
			case KindThinking:
				if thinkingParts != 2 || thinking != "Check the sample tool result." || e.Text != thinking || *e.Block != 0 {
					t.Fatalf("thinking final: %+v, accumulated %q", e, thinking)
				}
				thinkingFinal = true
			case KindToolStart:
				if !thinkingFinal || *e.Block != 1 || e.Tool.ID != "tool-sample-1" {
					t.Fatalf("tool start: %+v", e)
				}
			case KindToolInputDelta:
				if inputFinal || *e.Block != 1 {
					t.Fatalf("input attribution: %+v", e)
				}
				input += e.Tool.PartialJSON
				inputParts++
				if inputParts == 1 && (input != `{"file_path":"/tmp/work/sam` || json.Valid([]byte(input))) {
					t.Fatalf("partial input = %q", input)
				}
			case KindToolInput:
				if inputParts != 2 || *e.Block != 1 || e.Tool.ID != "tool-sample-1" || input != `{"file_path":"/tmp/work/sample.txt","content":"sample"}` || string(e.Tool.Input) != input {
					t.Fatalf("input final: %+v, accumulated %q", e, input)
				}
				inputFinal = true
			}
		}
		if raw.APIMessageID == "message-tool-1" && raw.Type == "stream_event" {
			var se streamEvent
			if err := json.Unmarshal(raw.Event, &se); err != nil {
				t.Fatal(err)
			}
			if se.Type == "content_block_stop" {
				if se.Index != stops {
					t.Fatalf("stop index = %d, want %d", se.Index, stops)
				}
				if _, open := n.open[raw.APIMessageID]; open {
					t.Fatal("block remains open after stop")
				}
				stops++
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if !thinkingFinal || !inputFinal || stops != 2 {
		t.Fatalf("finals thinking=%v input=%v stops=%d", thinkingFinal, inputFinal, stops)
	}
}
