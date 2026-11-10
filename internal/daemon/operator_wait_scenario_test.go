package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// Run the real poll, mail, and wait-clearing path with a spawn that occurs
// after the poll samples runs but before it reads the newly recorded wait.
func TestOperatorWaitSpawnScenario(t *testing.T) {
	raw, err := os.ReadFile("../../tests/evals/scenarios/operator-wait-spawn.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Cases []struct {
			Name         string
			First, Fresh map[string]bool
			Error, Fire  bool
		}
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	for _, tc := range sc.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			b := waitBead("waiting:100:operator:operator-op-build")
			var sent []sentMail
			w := watcher(b, nil, &sent, time.Unix(200, 0))
			calls := 0
			w.running = func(context.Context) (map[string]bool, error) {
				calls++
				if calls == 1 {
					return tc.First, nil
				}
				if tc.Error {
					return nil, errors.New("run store unavailable")
				}
				return tc.Fresh, nil
			}
			w.poll(context.Background())
			if (len(sent) == 1) != tc.Fire {
				t.Fatalf("mail=%+v fire=%v", sent, tc.Fire)
			}
			all := strings.Join(b.Calls, "\n")
			cleared := strings.Contains(all, "SetLabel:p-ws.1|label=waiting:100:operator:operator-op-build|on=false") ||
				strings.Contains(all, "--remove-label waiting:100:operator:operator-op-build")
			if cleared != tc.Fire {
				t.Fatalf("cleared=%v fire=%v calls=%v", cleared, tc.Fire, b.Calls)
			}

		})
	}
}
