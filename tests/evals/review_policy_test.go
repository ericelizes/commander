package evals

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/reviewpolicy"
)

func TestReviewPolicyScenario(t *testing.T) {
	b, err := os.ReadFile("scenarios/review-policy.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Cases []struct {
			Name, Path, Patch, Depth string
			Added, Deleted           int
			Full                     bool
			Files                    []reviewpolicy.File
		}
	}
	if err = json.Unmarshal(b, &scenario); err != nil {
		t.Fatal(err)
	}
	for _, c := range scenario.Cases {
		t.Run(c.Name, func(t *testing.T) {
			files := c.Files
			if len(files) == 0 {
				files = []reviewpolicy.File{{Path: c.Path, Added: c.Added, Deleted: c.Deleted}}
			}
			r := reviewpolicy.Select(files, c.Patch, c.Full)
			if r.Depth != c.Depth || r.Reason == "" || len(r.Files) != len(files) {
				t.Fatalf("got %+v; want %s", r, c.Depth)
			}
			if !strings.Contains(r.Adversarial, "no second primary roster") {
				t.Fatal("unbounded adversarial contract", r)
			}
		})
	}
}
