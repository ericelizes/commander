package widgets

import (
	"strings"
	"testing"
)

// TestMetrics_RendersAllFieldsWide drops a populated MetricsData into a
// generous-width card and verifies every numeric field appears in the output
// in its expected formatting.
func TestMetrics_RendersAllFieldsWide(t *testing.T) {
	m := NewMetrics()
	m.SetSize(60, 8)
	m.SetData(MetricsData{
		TotalCostUSD24h: 12.34,
		TotalCostUSD7d:  98.76,
		AgentCount:      8,
		ActiveCount:     3,
		BlockedCount:    1,
		IdleRatio:       0.6,
	})

	out := m.View()
	if out == "" {
		t.Fatal("View returned empty")
	}
	for _, want := range []string{"$12.34", "$98.76", "60%"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
	// Active and blocked counts both render as raw digits — check the labels
	// instead, which only appear in wide mode.
	for _, want := range []string{"cost", "agents", "idle"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide layout missing label %q\n%s", want, out)
		}
	}
}

// TestMetrics_NarrowDropsLabels confirms the <40 col path collapses the
// verbose labels (cost / agents / idle) to single-character icons. The
// numbers themselves must still appear.
func TestMetrics_NarrowDropsLabels(t *testing.T) {
	m := NewMetrics()
	m.SetSize(28, 8)
	m.SetData(MetricsData{
		TotalCostUSD24h: 1.50,
		TotalCostUSD7d:  10.00,
		AgentCount:      2,
		ActiveCount:     1,
		BlockedCount:    0,
		IdleRatio:       0.5,
	})
	out := m.View()
	if strings.Contains(out, "agents  active") {
		t.Errorf("narrow output should not contain wide-mode label, got:\n%s", out)
	}
	if !strings.Contains(out, "$1.50") {
		t.Errorf("narrow output dropped cost figure:\n%s", out)
	}
}

// TestMetrics_CostFormatTwoDecimals catches sloppy %g or default float
// formatting that would mis-align cents columns across rows.
func TestMetrics_CostFormatTwoDecimals(t *testing.T) {
	m := NewMetrics()
	m.SetSize(60, 8)
	m.SetData(MetricsData{TotalCostUSD24h: 7, TotalCostUSD7d: 0.5})
	out := m.View()
	if !strings.Contains(out, "$7.00") {
		t.Errorf("expected $7.00 zero-padded, got:\n%s", out)
	}
	if !strings.Contains(out, "$0.50") {
		t.Errorf("expected $0.50 with two decimals, got:\n%s", out)
	}
}

// TestMetrics_IdleColorTiers picks one ratio per tier and asserts the
// idleColor helper picks the documented ANSI palette index. The widget's
// visual output depends on this; the helper is the only stable seam to
// verify color tiers without parsing escape codes.
func TestMetrics_IdleColorTiers(t *testing.T) {
	cases := map[float64]string{
		0.75: "10", // green tier
		0.40: "11", // yellow tier
		0.10: "9",  // red tier
		0.0:  "9",  // boundary: zero idle
		0.5:  "10", // boundary: exactly half
		0.25: "11", // boundary: exactly quarter
	}
	for ratio, want := range cases {
		if got := idleColor(ratio); got != want {
			t.Errorf("idleColor(%.2f) = %q, want %q", ratio, got, want)
		}
	}
}

// TestMetrics_EmptyBeforeSize keeps the App-level no-op contract: View must
// be safe to call before SetSize fires.
func TestMetrics_EmptyBeforeSize(t *testing.T) {
	m := NewMetrics()
	if got := m.View(); got != "" {
		t.Errorf("unsized metrics should render empty, got %q", got)
	}
}
