package boardturn

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	// quoted is text the reply quotes rather than claims: code, quotes, blockquote lines.
	quoted   = regexp.MustCompile("(?s)```.*?```|`[^`\n]*`|\"[^\"\n]*\"|“[^”\n]*”|(?m)^\\s*>.*$")
	clauses  = regexp.MustCompile(`[.!?;]+(\s|$)|\n+|\s(?:but|while|though|although|whereas)\s`)
	rowRef   = regexp.MustCompile(`(?i)(^|[^\w#])#(\d+)([a-z])?\b`)
	claimFor = regexp.MustCompile(`(?i)\b(fixed|accurate|resolved|unblocked|green)\b`)
	// listJoin is what may sit between refs that share the claim after them ("#3 and #4 are fixed").
	listJoin = regexp.MustCompile(`(?i)^[\s,&/]*(and|or)?[\s,&/]*$`)
	negators = map[string]bool{"not": true, "never": true, "no": true, "isn't": true, "aren't": true, "wasn't": true, "won't": true,
		"can't": true, "cannot": true, "until": true, "once": true, "if": true, "when": true, "after": true, "will": true,
		"would": true, "should": true, "could": true, "yet": true, "unless": true, "before": true, "nothing": true}
	notRows = map[string]bool{"ask": true, "pr": true, "issue": true, "pull": true, "item": true}
)

// CheckClaims is the Stop-hook refusal for a reply that calls a row fixed
// (accurate, resolved, unblocked, green) while the board says it is stalled
// or stuck; "" when the reply makes no such claim. Quoted text is skipped, and
// so are negated or conditional claims ("not fixed", "once it is fixed").
func CheckClaims(f File, reply string) string {
	byLabel := map[string]Entry{}
	for _, e := range f.Entries {
		if !e.Ask {
			byLabel[strings.ToUpper(e.Label)] = e
		}
	}
	reply = quoted.ReplaceAllString(reply, " ")
	var out []string
	flagged := map[string]bool{}
	start := 0
	var parts []string
	for _, m := range append(clauses.FindAllStringIndex(reply, -1), []int{len(reply), len(reply)}) {
		parts = append(parts, reply[start:m[1]])
		start = m[1]
	}
	for _, clause := range parts {
		// A question asks; it does not report.
		if strings.Contains(clause, "?") {
			continue
		}
		refs := rowRef.FindAllStringSubmatchIndex(clause, -1)
		var pending []string // refs listed before the claim that covers them
		for i, m := range refs {
			label := strings.ToUpper("#" + clause[m[4]:m[5]])
			if m[6] >= 0 {
				label += strings.ToUpper(clause[m[6]:m[7]])
			}
			if before := strings.Fields(strings.ToLower(clause[:m[3]])); len(before) > 0 && notRows[strings.Trim(before[len(before)-1], ".,:;()")] {
				continue
			}
			end := len(clause)
			if i+1 < len(refs) {
				end = refs[i+1][0]
			}
			seg := clause[m[1]:end]
			pending = append(pending, label)
			word := claimIn(clause[:m[1]], seg)
			if word == "" {
				if i+1 < len(refs) && listJoin.MatchString(seg) {
					continue
				}
				pending = nil
				continue
			}
			for _, l := range pending {
				e, ok := byLabel[l]
				if !ok || !e.broken() || flagged[l] {
					continue
				}
				flagged[l] = true
				out = append(out, fmt.Sprintf("Board v%d %s reads %q. Your reply says it is %s. Fix it or correct the report.", f.Version, l, e.Text, word))
			}
			pending = nil
		}
	}
	return strings.Join(out, "\n")
}

// claimIn is the first claim word in seg that nothing just before it (in
// lead, then seg) negates.
func claimIn(lead, seg string) string {
	for _, m := range claimFor.FindAllStringIndex(seg, -1) {
		words := strings.Fields(strings.ToLower(lead + seg[:m[0]]))
		if len(words) > 4 {
			words = words[len(words)-4:]
		}
		negated := false
		for i, w := range words {
			w = strings.Trim(w, ".,;:()'")
			if negators[w] || strings.HasSuffix(w, "n't") || strings.HasSuffix(w, "'ll") || (w == "be" && i > 0 && strings.Trim(words[i-1], ".,") == "to") {
				negated = true
			}
		}
		if !negated {
			return strings.ToLower(seg[m[0]:m[1]])
		}
	}
	return ""
}
