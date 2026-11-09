package evals

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/example-user/commander/internal/managerguard"
	"github.com/example-user/commander/internal/operatorguard"
)

// Classifier-only probes: no shell commands or credential reads are executed.
func TestCredentialGlobsScenario(t *testing.T) {
	t.Setenv("HOME", "/tmp/user-home")
	t.Setenv("COMMANDER_HOME", "/tmp/private")
	raw, err := os.ReadFile("scenarios/credential-globs.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Cases []struct {
			Command string
			Deny    bool
		}
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	if len(scenario.Cases) == 0 {
		t.Fatal("empty scenario")
	}
	for _, tc := range scenario.Cases {
		m := managerguard.Classify(tc.Command, "")
		o := operatorguard.Classify(tc.Command)
		if m.Deny != tc.Deny || o.Deny != tc.Deny || tc.Deny && !o.Credential {
			t.Errorf("%q: manager=%+v operator=%+v, want deny=%v", tc.Command, m, o, tc.Deny)
		}
	}
}
