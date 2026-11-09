package ceilings

import "testing"

// runStateFor builds a runState directly for enforce tests — no goroutine
// lifecycle, no Register, just the math.
func runStateFor(usd float64, tokens int64, failures int) *runState {
	return &runState{
		runID:               99,
		tokensUsed:          tokens,
		usdUsed:             usd,
		consecutiveFailures: failures,
	}
}

func TestCheckCeilings_USDBreach(t *testing.T) {
	w := &Watchdog{limits: Limits{
		MaxUSDPerOperator:        5.0,
		MaxTokensPerOperator:     200_000,
		SingleCallUSDEstimate:    0.5,
		SingleCallTokensEstimate: 8_000,
	}}
	st := runStateFor(5.01, 100, 0)
	kill, reason := w.checkCeilings(st)
	if !kill {
		t.Fatalf("expected kill on usd breach")
	}
	if reason == "" {
		t.Fatalf("expected non-empty reason")
	}
}

func TestCheckCeilings_TokenBreach(t *testing.T) {
	w := &Watchdog{limits: Limits{
		MaxUSDPerOperator:        5.0,
		MaxTokensPerOperator:     200_000,
		SingleCallUSDEstimate:    0.5,
		SingleCallTokensEstimate: 8_000,
	}}
	st := runStateFor(0.01, 200_000, 0)
	kill, _ := w.checkCeilings(st)
	if !kill {
		t.Fatalf("expected kill on token breach")
	}
}

func TestCheckCeilings_USDPreemptive(t *testing.T) {
	w := &Watchdog{limits: Limits{
		MaxUSDPerOperator:        5.0,
		MaxTokensPerOperator:     200_000,
		SingleCallUSDEstimate:    0.5,
		SingleCallTokensEstimate: 8_000,
	}}
	// 4.6 spent → 0.4 remaining < 0.5 estimate → pre-emptive halt.
	st := runStateFor(4.6, 100, 0)
	kill, reason := w.checkCeilings(st)
	if !kill {
		t.Fatalf("expected pre-emptive halt")
	}
	if reason == "" || !contains(reason, "pre-emptive") {
		t.Fatalf("expected pre-emptive reason, got %q", reason)
	}
}

func TestCheckCeilings_TokenPreemptive(t *testing.T) {
	w := &Watchdog{limits: Limits{
		MaxUSDPerOperator:        5.0,
		MaxTokensPerOperator:     200_000,
		SingleCallUSDEstimate:    0.5,
		SingleCallTokensEstimate: 8_000,
	}}
	// 195_000 spent → 5_000 remaining < 8_000 estimate.
	st := runStateFor(0.01, 195_000, 0)
	kill, reason := w.checkCeilings(st)
	if !kill {
		t.Fatalf("expected pre-emptive halt")
	}
	if !contains(reason, "pre-emptive") {
		t.Fatalf("expected pre-emptive reason, got %q", reason)
	}
}

func TestCheckCeilings_RetryBreach(t *testing.T) {
	w := &Watchdog{limits: Limits{
		MaxUSDPerOperator:        5.0,
		MaxTokensPerOperator:     200_000,
		MaxRetries:               3,
		SingleCallUSDEstimate:    0.5,
		SingleCallTokensEstimate: 8_000,
	}}
	st := runStateFor(0.01, 100, 3)
	kill, _ := w.checkCeilings(st)
	if !kill {
		t.Fatalf("expected kill on retry ceiling")
	}
}

func TestCheckCeilings_UnderAllLimits(t *testing.T) {
	w := &Watchdog{limits: Limits{
		MaxUSDPerOperator:        5.0,
		MaxTokensPerOperator:     200_000,
		MaxRetries:               3,
		SingleCallUSDEstimate:    0.5,
		SingleCallTokensEstimate: 8_000,
	}}
	st := runStateFor(2.0, 50_000, 1)
	kill, reason := w.checkCeilings(st)
	if kill {
		t.Fatalf("did not expect kill, got reason %q", reason)
	}
}

func TestCheckCeilings_ZeroLimitsMeansUnlimited(t *testing.T) {
	w := &Watchdog{limits: Limits{}}
	st := runStateFor(1_000_000, 1_000_000_000, 100)
	kill, reason := w.checkCeilings(st)
	if kill {
		t.Fatalf("zero limits = unlimited; got kill reason %q", reason)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
