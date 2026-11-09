package ceilings

import "fmt"

// checkCeilings inspects an updated runState and returns (kill, reason). The
// caller holds NO lock — checkCeilings takes the per-state mutex briefly.
//
// Order of checks is deliberate: hard breaches first so the reason names the
// actual violation, then pre-emptive halt (which is a softer, forward-looking
// gate). The retry ceiling is checked here too even though applyError owns
// the increment — defence in depth: a step_finish that follows several
// errors without a fresh success would otherwise wait for the next error
// before tripping.
func (w *Watchdog) checkCeilings(st *runState) (bool, string) {
	st.mu.Lock()
	tokens := st.tokensUsed
	usd := st.usdUsed
	failures := st.consecutiveFailures
	st.mu.Unlock()

	limits := w.limits

	// Hard breach: USD.
	if limits.MaxUSDPerOperator > 0 && usd >= limits.MaxUSDPerOperator {
		return true, fmt.Sprintf("usd ceiling: $%.2f >= $%.2f", usd, limits.MaxUSDPerOperator)
	}
	// Hard breach: tokens.
	if limits.MaxTokensPerOperator > 0 && tokens >= limits.MaxTokensPerOperator {
		return true, fmt.Sprintf("token ceiling: %d >= %d", tokens, limits.MaxTokensPerOperator)
	}
	// Pre-emptive halt: USD remaining < single-call estimate.
	if limits.MaxUSDPerOperator > 0 && limits.SingleCallUSDEstimate > 0 {
		if limits.MaxUSDPerOperator-usd < limits.SingleCallUSDEstimate {
			return true, fmt.Sprintf(
				"usd ceiling near (pre-emptive halt): $%.2f remaining, $%.2f single-call estimate",
				limits.MaxUSDPerOperator-usd, limits.SingleCallUSDEstimate,
			)
		}
	}
	// Pre-emptive halt: tokens remaining < single-call estimate.
	if limits.MaxTokensPerOperator > 0 && limits.SingleCallTokensEstimate > 0 {
		if limits.MaxTokensPerOperator-tokens < limits.SingleCallTokensEstimate {
			return true, fmt.Sprintf(
				"token ceiling near (pre-emptive halt): %d remaining, %d single-call estimate",
				limits.MaxTokensPerOperator-tokens, limits.SingleCallTokensEstimate,
			)
		}
	}
	// Retry ceiling.
	if limits.MaxRetries > 0 && failures >= limits.MaxRetries {
		return true, fmt.Sprintf("retry ceiling: %d consecutive failures", failures)
	}
	return false, ""
}
