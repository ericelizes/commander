package opencode

import (
	"context"
	"strings"
	"testing"
)

func TestParseTotalSpend(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want float64
	}{
		{"simple", "Total: $1.23\n", 1.23},
		{"phrase", "Total spend: $4.56 over 7 days", 4.56},
		{"no match", "no dollars here", 0},
		{"integer", "Total: $5", 5},
		{"empty", "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseTotalSpend(c.in)
			if got != c.want {
				t.Errorf("parseTotalSpend(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestStats_Integration(t *testing.T) {
	// Fake opencode binary that prints a tiny stats blurb.
	script := `#!/bin/sh
cat <<'TXT'
opencode stats — last 7 days
Project: /code/commander
Total spend: $2.34
TXT
`
	bin := writeFakeBinary(t, "opencode", script)
	c := NewWithBinary(bin)

	got, err := c.Stats(context.Background(), "/code/commander", 7)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if !strings.Contains(got.Raw, "last 7 days") {
		t.Errorf("Raw missing expected text: %q", got.Raw)
	}
	if got.TotalSpendUSD != 2.34 {
		t.Errorf("TotalSpendUSD = %v, want 2.34", got.TotalSpendUSD)
	}
	if got.DaysBack != 7 {
		t.Errorf("DaysBack = %d", got.DaysBack)
	}
	if got.Project != "/code/commander" {
		t.Errorf("Project = %q", got.Project)
	}
}
