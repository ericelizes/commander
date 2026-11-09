package daemon

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/livedata"
)

func TestMissingTicketNoticeScenario(t *testing.T) {
	raw, err := os.ReadFile("../../tests/evals/scenarios/missing-ticket-notice.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct{ Title, Expected, Forbidden string }
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	got := linearLines(t, livedata.SnapWorkstream{Project: "p", BeadID: "p-1", Title: sc.Title}, 0)["no-ticket:p:p-1"]
	if !strings.Contains(got.Text, sc.Expected) || strings.Contains(got.Text, sc.Forbidden) {
		t.Fatalf("notice=%+v", got)
	}
	if !strings.Contains(got.Agent, "Find its ticket") {
		t.Fatalf("missing actionable instruction: %+v", got)
	}
}
