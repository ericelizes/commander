package beads

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestShowMultilineTitleScenario(t *testing.T) {
	raw, err := os.ReadFile("../../tests/evals/scenarios/show-multiline-title.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Cases []struct{ Title, Status string }
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	for _, tc := range sc.Cases {
		t.Run(tc.Status, func(t *testing.T) {
			js, _ := json.Marshal(map[string]any{"id": "p-1", "title": tc.Title, "status": tc.Status})
			text := fmt.Sprintf("○ p-1 · %s   [P2 · %s]\n", tc.Title, tc.Status)
			c := &cmdClient{run: showExec(text, js)}
			b, err := c.Show(context.Background(), "/p", "p-1")
			if err != nil {
				t.Fatal(err)
			}
			if b.Title != tc.Title || b.Status != tc.Status {
				t.Fatalf("got=%+v", b)
			}
		})
	}
}

func TestShowTitleAndStatusFallbackWhenJSONUnavailable(t *testing.T) {
	for _, js := range [][]byte{nil, []byte(`{"id":"p-1"}`)} {
		c := &cmdClient{run: showExec("○ p-1 · Choose a region   [P2 · OPEN]\n", js)}
		b, err := c.Show(context.Background(), "/p", "p-1")
		if err != nil {
			t.Fatal(err)
		}
		if b.Title != "Choose a region" || b.Status != "open" {
			t.Fatalf("fallback=%+v", b)
		}
	}
}
