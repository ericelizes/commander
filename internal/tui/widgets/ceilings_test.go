package widgets

import (
	"strings"
	"testing"
)

// TestCeilings_RendersAllThreeLines confirms the three documented dimensions
// (usd / tok / wall) all appear in the output.
func TestCeilings_RendersAllThreeLines(t *testing.T) {
	c := NewCeilings()
	c.SetSize(60, 5)
	c.SetData(CeilingsData{
		USDUsed: 1, USDMax: 5,
		TokensUsed: 1000, TokensMax: 10000,
		WallclockMin: 5, WallclockMax: 60,
	})
	out := c.View()
	for _, want := range []string{"usd", "tok", "wall", "$1.00", "$5.00", "1,000", "10,000", "5m", "60m"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d:\n%s", len(lines), out)
	}
}

// TestCeilings_BarFillProportional checks a 50% fill renders ~half blocks.
// We count the █ glyphs against ░ to avoid coupling to bar-width math.
func TestCeilings_BarFillProportional(t *testing.T) {
	c := NewCeilings()
	c.SetSize(60, 5)
	c.SetData(CeilingsData{USDUsed: 1, USDMax: 2}) // 50%
	out := c.View()

	// Pull just the usd line.
	usdLine := strings.Split(out, "\n")[0]
	filled := strings.Count(usdLine, "█")
	empty := strings.Count(usdLine, "░")
	if filled == 0 || empty == 0 {
		t.Fatalf("expected mixed bar at 50%%, got %q", usdLine)
	}
	// At 50% the two should be within 1 of each other (rounding).
	diff := filled - empty
	if diff < -1 || diff > 1 {
		t.Errorf("50%% bar unbalanced: filled=%d empty=%d line=%q", filled, empty, usdLine)
	}
}

// TestCeilings_ZeroMaxNoDivByZero protects against the "ceiling not
// configured" path. The widget must render an empty bar, not crash, not
// emit Inf%.
func TestCeilings_ZeroMaxNoDivByZero(t *testing.T) {
	c := NewCeilings()
	c.SetSize(60, 5)
	c.SetData(CeilingsData{USDUsed: 1.23, USDMax: 0})

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("View panicked on zero max: %v", r)
		}
	}()

	out := c.View()
	if strings.Contains(out, "Inf") || strings.Contains(out, "NaN") {
		t.Errorf("zero-max output leaked NaN/Inf: %q", out)
	}
	usdLine := strings.Split(out, "\n")[0]
	if strings.Count(usdLine, "█") != 0 {
		t.Errorf("zero-max bar should be empty, got %q", usdLine)
	}
}

// TestCeilings_ColorTiers asserts the threshold map matches the spec:
// <50% green, 50–80% yellow, 80%+ red. The helper is the only stable seam.
func TestCeilings_ColorTiers(t *testing.T) {
	cases := map[float64]string{
		0.10: "10",
		0.49: "10",
		0.50: "11",
		0.79: "11",
		0.80: "9",
		0.95: "9",
		1.50: "9",
	}
	for fill, want := range cases {
		if got := ceilingColor(fill); got != want {
			t.Errorf("ceilingColor(%.2f) = %q, want %q", fill, got, want)
		}
	}
}

// TestCeilings_RatioGuard exercises the divide-by-zero guard directly.
func TestCeilings_RatioGuard(t *testing.T) {
	if r := ratio(5, 0); r != 0 {
		t.Errorf("ratio(5,0) = %v, want 0", r)
	}
	if r := ratio(1, 4); r != 0.25 {
		t.Errorf("ratio(1,4) = %v, want 0.25", r)
	}
}

// TestCeilings_RenderBarBoundaries makes sure clamping behaves at the edges.
func TestCeilings_RenderBarBoundaries(t *testing.T) {
	if got := renderBar(0, 0.5); got != "" {
		t.Errorf("zero-width bar should be empty, got %q", got)
	}
	full := renderBar(10, 1.0)
	if strings.Count(full, "█") != 10 {
		t.Errorf("full bar should be all blocks, got %q", full)
	}
	over := renderBar(10, 5.0)
	if strings.Count(over, "█") != 10 {
		t.Errorf("over-1.0 fill should clamp to full, got %q", over)
	}
	under := renderBar(10, -1.0)
	if strings.Count(under, "█") != 0 {
		t.Errorf("negative fill should clamp to empty, got %q", under)
	}
}

// TestCeilings_FormatIntThousands is small but load-bearing — token counts
// hit 6+ digits frequently and unseparated numbers misread fast.
func TestCeilings_FormatIntThousands(t *testing.T) {
	cases := map[int]string{
		0:       "0",
		999:     "999",
		1000:    "1,000",
		12345:   "12,345",
		1234567: "1,234,567",
		-1234:   "-1,234",
	}
	for n, want := range cases {
		if got := formatInt(n); got != want {
			t.Errorf("formatInt(%d) = %q, want %q", n, got, want)
		}
	}
}
