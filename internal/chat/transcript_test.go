package chat

import (
	"strings"
	"testing"
)

// The synthetic transcript covers denial, Bash, interruption and compaction.
func TestReadTranscript(t *testing.T) {
	events, info, err := ReadTranscript("testdata/transcript_synthetic.jsonl", "manager-x")
	if err != nil {
		t.Fatal(err)
	}
	if !info.CostKnown || info.TotalCostUSD < 0.0399 || info.TotalCostUSD > 0.0401 {
		t.Fatalf("cost baseline = %+v", info)
	}
	if info.Cwd != "/tmp/work" {
		t.Fatalf("cwd = %q", info.Cwd)
	}
	for _, e := range events {
		if e.Source != SourceTranscript || e.Role != "manager-x" || e.SessionID != "00000000-0000-4000-8000-000000000001" || e.Time.IsZero() {
			t.Fatalf("bad event header: %+v", e)
		}
	}
	users := kinds(events, KindUser)
	if len(users) < 4 || !strings.HasPrefix(users[0].Text, "Use the Write tool") || users[0].Origin != "transcript" {
		t.Fatalf("user messages = %+v", users)
	}
	var slash []string
	for _, u := range users {
		if u.Subtype == "slash_command" {
			slash = append(slash, u.Text)
		}
	}
	if strings.Join(slash, ",") != "/context,/compact,/usage" {
		t.Fatalf("slash commands = %v", slash)
	}
	results := kinds(events, KindToolResult)
	if len(results) != 3 || !results[0].Tool.IsError || results[0].Tool.Name != "Write" || results[1].Tool.Name != "Bash" || len(results[1].Tool.Structured) == 0 {
		t.Fatalf("tool results = %+v", results)
	}
	if len(kinds(events, KindToolInput)) != 3 || len(kinds(events, KindToolStart)) != 3 {
		t.Fatal("want 3 tool calls")
	}
	texts := kinds(events, KindText)
	if len(texts) < 4 || texts[len(texts)-1].Text == "" {
		t.Fatalf("assistant texts = %+v", texts)
	}
	if len(kinds(events, KindCompaction)) != 1 {
		t.Fatal("want one compaction")
	}
	var notices []string
	for _, n := range kinds(events, KindNotice) {
		notices = append(notices, n.Subtype)
	}
	got := strings.Join(notices, ",")
	for _, want := range []string{"interrupted", "compact_summary", "local_command"} {
		if !strings.Contains(got, want) {
			t.Fatalf("notices %s lack %s", got, want)
		}
	}
}

func TestFindTranscript(t *testing.T) {
	dir := t.TempDir()
	if FindTranscript(dir, "abc") != "" {
		t.Fatal("found a missing transcript")
	}
	writeTranscript(t, dir, "abc", "{}\n")
	if !strings.HasSuffix(FindTranscript(dir, "abc"), "/abc.jsonl") {
		t.Fatal("transcript not found")
	}
}
