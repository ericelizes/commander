package store

import (
	"context"
	"strings"
	"testing"
)

func TestParseDecision(t *testing.T) {
	d, err := ParseDecision(`{"question":"Merge #4107?","context":"CI green","options":[{"label":"Merge","implications":"ships","recommended":true},{"label":"Wait"}],"input":true}`)
	if err != nil || d.Options[0].Label != "Merge" || !d.Options[0].Recommended || !d.Input {
		t.Fatalf("got %+v, %v", d, err)
	}
	for name, raw := range map[string]string{
		"no question": `{"options":[{"label":"x"}]}`,
		"nothing":     `{"question":"q"}`,
		"unlabeled":   `{"question":"q","options":[{"label":" "}]}`,
		"unknown":     `{"question":"q","options":[{"label":"x"}],"bogus":1}`,
		"not json":    `merge it`,
		"too many":    `{"question":"q","options":[` + strings.Repeat(`{"label":"x"},`, 9) + `{"label":"y"}]}`,
	} {
		if _, err := ParseDecision(raw); err == nil {
			t.Errorf("%s should be rejected", name)
		}
	}
}

func TestNeedsYou_DecisionRoundTripsAndSurvivesAnAsklessUpsert(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	if err := s.UpsertNeedsYou(ctx, NeedsYou{ProjectID: 1, BeadID: "b", Ask: "Merge?", Decision: `{"question":"Merge?"}`}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertNeedsYou(ctx, NeedsYou{ProjectID: 1, BeadID: "b", Ask: "Merge?"}); err != nil {
		t.Fatal(err)
	}
	n, _ := s.GetNeedsYou(ctx, 1, "b")
	if n == nil || n.Decision != `{"question":"Merge?"}` {
		t.Errorf("decision lost: %+v", n)
	}
}
