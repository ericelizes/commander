package gates

import "testing"

// A check is the standard merged template only when every part is exactly its form.
func TestIsMergedCheck(t *testing.T) {
	for _, check := range []string{
		MergedCheck([]string{"7"}, ""),
		MergedCheck([]string{"7"}, "acme/web.app"),
		MergedCheck([]string{"7", "8"}, "acme/web"),
	} {
		if !IsMergedCheck(check) {
			t.Errorf("standard check refused: %s", check)
		}
	}
	for _, check := range []string{
		"", "true",
		MergedCheck([]string{"7"}, "acme/web") + " || true",
		MergedCheck([]string{"7"}, "acme/web") + " && true",
		"true && " + MergedCheck([]string{"7"}, "acme/web"),
		`[ "$(gh pr view 7; true --json state --jq .state)" = MERGED ]`,
		`[ "$(gh pr view 7 --json state --jq .state)" = OPEN ]`,
		MergedCheck([]string{"$(true)"}, ""),
	} {
		if IsMergedCheck(check) {
			t.Errorf("redefined check accepted: %s", check)
		}
	}
}
