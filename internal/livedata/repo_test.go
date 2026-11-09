package livedata

import "testing"

func TestParseGitHubRepo(t *testing.T) {
	for url, want := range map[string]string{
		"https://github.com/acme/example-app.git": "acme/example-app",
		"git@github.com:alice/commander.git":      "alice/commander",
		"https://github.com/o/r":                  "o/r",
		"https://gitlab.com/o/r.git":              "",
		"":                                        "",
	} {
		if got := ParseGitHubRepo(url); got != want {
			t.Errorf("%q -> %q, want %q", url, got, want)
		}
	}
}
