package store

import (
	"regexp"
	"strconv"
	"strings"
)

// canonicalPredicate retains atoms verbatim and normalizes only associative
// Boolean grouping. AND and OR remain distinct nodes, preserving precedence.
// Catalog SQL uses single-quoted strings and double-quoted identifiers; neither
// their parentheses nor their keywords are syntax for this walker.
func canonicalPredicate(s string) string {
	s = strings.TrimSpace(s)
	for strings.HasPrefix(s, "(") && predicateClose(s, 0) == len(s)-1 {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	for _, op := range []string{" OR ", " AND "} {
		var parts []string
		start := 0
		for i := 0; i < len(s); {
			if s[i] == '\'' || s[i] == '"' {
				i = predicateQuoteEnd(s, i)
				continue
			}
			if s[i] == '(' {
				end := predicateClose(s, i)
				if end < 0 {
					return s
				}
				i = end + 1
				continue
			}
			if strings.HasPrefix(s[i:], op) {
				parts = append(parts, canonicalPredicate(s[start:i]))
				i += len(op)
				start = i
				continue
			}
			i++
		}
		if len(parts) > 0 {
			parts = append(parts, canonicalPredicate(s[start:]))
			// Child nodes of the same operator can be flattened without changing
			// SQL's three-valued Boolean result or operand order.
			prefix := strings.TrimSpace(op) + "["
			for i, p := range parts {
				if strings.HasPrefix(p, prefix) {
					parts[i] = p[len(prefix) : len(p)-1]
				}
			}
			return prefix + strings.Join(parts, ",") + "]"
		}
	}
	return "atom" + strconv.Itoa(len(s)) + ":" + s
}
func predicateQuoteEnd(s string, i int) int {
	quote := s[i]
	i++
	for i < len(s) {
		if s[i] == quote {
			i++
			if i < len(s) && s[i] == quote {
				i++
				continue
			}
			return i
		}
		i++
	}
	return i
}
func predicateClose(s string, start int) int {
	depth := 0
	for i := start; i < len(s); i++ {
		if s[i] == '\'' || s[i] == '"' {
			i = predicateQuoteEnd(s, i) - 1
			continue
		}
		if s[i] == '(' {
			depth++
		}
		if s[i] == ')' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

var distinctPredicateAtom = regexp.MustCompile(`old\.[a-z_][a-z_0-9]* IS DISTINCT FROM new\.[a-z_][a-z_0-9]*`)

func normalizeTriggerPredicate(s string) string {
	const marker = " WHEN ("
	start := strings.Index(s, marker)
	if start < 0 {
		return s
	}
	start += len(marker) - 1
	end := predicateClose(s, start)
	if end < 0 {
		return s
	}
	// Restrict canonicalization to the catalog grammar used by our row-change
	// triggers. Unknown expressions keep their exact fingerprint; this is not a
	// general SQL parser (NOT, CASE and BETWEEN need their own grammar).
	remainder := distinctPredicateAtom.ReplaceAllString(s[start:end+1], "")
	remainder = strings.NewReplacer(" OR ", "", " AND ", "", "(", "", ")", "", " ", "").Replace(remainder)
	if remainder != "" {
		return s
	}
	return s[:start] + canonicalPredicate(s[start:end+1]) + s[end+1:]
}
