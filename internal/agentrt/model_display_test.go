package agentrt

import "testing"

func TestModelDisplay(t *testing.T) {
	cases := map[string]string{
		"sonnet":                      "Sonnet 5.5",
		"anthropic/claude-sonnet-4-7": "Sonnet 5.5", // launches as the alias, which tracks the latest
		"anthropic/claude-opus-4-5":   "Opus 5.5",
		"haiku":                       "Haiku 5.5",
		"claude-fable-5-1":            "Fable 5.1",
		"":                            "",
		"openai/gpt-5":                "", // non-Anthropic: Claude's default applies, nothing to show
	}
	for in, want := range cases {
		if got := ModelDisplay(in); got != want {
			t.Errorf("ModelDisplay(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrettyModelID(t *testing.T) {
	cases := map[string]string{
		"claude-sonnet-5-5":        "Sonnet 5.5",
		"claude-opus-4-1-20250805": "Opus 4.1",
		"claude-3-haiku":           "claude-3-haiku", // not parseable: unchanged
		"weird":                    "weird",
	}
	for in, want := range cases {
		if got := PrettyModelID(in); got != want {
			t.Errorf("PrettyModelID(%q) = %q, want %q", in, got, want)
		}
	}
}
