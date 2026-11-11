package evals

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/example-user/commander/internal/managerguard"
	"github.com/example-user/commander/internal/operatorguard"
)

// Retained command shapes are data only, never subprocess input.
func TestSourceSearchScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/source-search/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Command string
		Deny    bool
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("empty scenario")
	}
	for _, tc := range cases {
		m, o := managerguard.Classify(tc.Command, ""), operatorguard.Classify(tc.Command)
		if m.Deny != tc.Deny || o.Deny != tc.Deny {
			t.Errorf("%q: manager=%+v operator=%+v want deny=%v", tc.Command, m, o, tc.Deny)
		}
	}
}
