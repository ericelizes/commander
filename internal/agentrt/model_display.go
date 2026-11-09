package agentrt

import (
	"strings"
	"unicode"
)

// latestByFamily is what each model ALIAS resolves to today. Claude Code's
// aliases (opus, sonnet, ...) always track the newest release, so this table is
// the one place to bump when a new release lands; ids that are not aliases are
// prettified from the id itself.
var latestByFamily = map[string]string{
	"opus":   "Opus 5.5",
	"sonnet": "Sonnet 5.5",
	"haiku":  "Haiku 5.5",
	"fable":  "Fable 5.1",
}

// ModelDisplay is the short name to show for a configured model: the model that
// is actually running, e.g. "Sonnet 5.5" for "anthropic/claude-sonnet-4-7" (which
// launches as the `sonnet` alias) or for the alias itself.
func ModelDisplay(model string) string {
	m := ClaudeModel(model)
	if m == "" {
		return ""
	}
	if d, ok := latestByFamily[strings.ToLower(m)]; ok {
		return d
	}
	return PrettyModelID(m)
}

// PrettyModelID turns "claude-sonnet-5-5" into "Sonnet 5.5" and
// "claude-opus-4-1-20250805" into "Opus 4.1". Anything it cannot parse comes
// back unchanged.
func PrettyModelID(id string) string {
	parts := strings.Split(strings.TrimPrefix(id, "claude-"), "-")
	if len(parts) < 2 {
		return id
	}
	family := parts[0]
	var version []string
	for _, p := range parts[1:] {
		if len(p) >= 8 { // a date stamp ends the version
			break
		}
		if p == "" || !unicode.IsDigit(rune(p[0])) {
			return id
		}
		version = append(version, p)
	}
	if len(version) == 0 || family == "" {
		return id
	}
	return strings.ToUpper(family[:1]) + family[1:] + " " + strings.Join(version, ".")
}
