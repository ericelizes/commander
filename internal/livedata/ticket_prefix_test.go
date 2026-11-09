package livedata

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPlainTitleDropsOnlyTheConfiguredTicketPrefix(t *testing.T) {
	t.Cleanup(func() { SetTicketPrefix("") })
	cases := []struct{ prefix, in, want string }{
		{"team", "Spike the spec (TEAM-101)", "Spike the spec"},
		{"TEAM", "Ship (TEAM-1) now", "Ship now"},
		{"TEAM", "Decode input as (UTF-8)", "Decode input as (UTF-8)"},
		{"TEAM", "Hash files with (SHA-256)", "Hash files with (SHA-256)"},
		{"TEAM", "Dates in (ISO-8601)", "Dates in (ISO-8601)"},
		{"TEAM", "Other team (OPS-12)", "Other team (OPS-12)"},
		{"TEAM", "Too long (TEAM-1234567)", "Too long (TEAM-1234567)"},
		{"", "Spike the spec (TEAM-101)", "Spike the spec (TEAM-101)"},
		{"not a key", "Spike the spec (TEAM-101)", "Spike the spec (TEAM-101)"},
	}
	for _, c := range cases {
		SetTicketPrefix(c.prefix)
		if got := plainTitle(c.in); got != c.want {
			t.Errorf("prefix %q: plainTitle(%q) = %q, want %q", c.prefix, c.in, got, c.want)
		}
	}
}

func TestLoadTicketPrefixReadsTheLinearTeam(t *testing.T) {
	t.Cleanup(func() { SetTicketPrefix("") })
	home := t.TempDir()
	LoadTicketPrefix(home)
	if got := plainTitle("Spike (TEAM-1)"); got != "Spike (TEAM-1)" {
		t.Fatalf("no local.toml: %q", got)
	}
	if err := os.WriteFile(filepath.Join(home, "local.toml"), []byte("[integrations.linear]\nteam = 'TEAM'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	LoadTicketPrefix(home)
	if got := plainTitle("Spike (TEAM-1) in (UTF-8)"); got != "Spike in (UTF-8)" {
		t.Fatalf("team TEAM: %q", got)
	}
}
