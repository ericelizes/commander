package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/store"
)

// 3.4 on Nov 17: the Grafana-live agent asked Eric for Slack channel ids. A
// question asked --to user becomes his card, in the agent's words.
func TestUserQuestionCard(t *testing.T) {
	q := store.OpenQuestion{ID: "e7d7", Project: "vibe-apps", Bead: "vibe-apps-596t.12.10", Title: "Grafana alerting live",
		Question: "Need Slack channel IDs (not names) set in the mgmt-notify role, then a deploy", Recommendation: "Use #forge-admin-alerts"}
	ask, decision := questionCard(q)
	d, err := store.ParseDecision(decision)
	if ask != store.QuestionCardAsk || err != nil || !d.Input || !strings.Contains(d.Question, "Slack channel IDs") || !strings.Contains(d.Context, "#forge-admin-alerts") {
		t.Fatalf("%q %+v %v", ask, d, err)
	}
	var raw map[string]any
	if json.Unmarshal([]byte(decision), &raw) != nil {
		t.Fatal(decision)
	}
}

// Questions go to the Commander unless --to user; anything else is refused
// before anything is recorded, and a question to the user always blocks.
func TestQuestionAskTo(t *testing.T) {
	for _, args := range [][]string{
		{"ask", "--project-id", "1", "--bead", "b", "--to", "eric"},
		{"ask", "--project-id", "1", "--bead", "b", "--to", "user", "--blocking=false"},
	} {
		var out, errb bytes.Buffer
		if code := runQuestion(args, &out, &errb); code != 2 || errb.Len() == 0 {
			t.Fatalf("%v: %d %q", args, code, errb.String())
		}
	}
}
