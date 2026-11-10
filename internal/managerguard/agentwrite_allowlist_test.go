package managerguard

import "testing"

// The guard is an allow-list of read verbs: a bd/br verb that is not known to read is refused,
// including ones the old deny-list never named.
func TestGuardAllowsOnlyReads(t *testing.T) {
	reads := []string{
		"bd show x-1", "bd list --json", "bd ready", "br --db /p/.beads/beads.db show x-1", "bd comments x-1", "bd comments list x-1",
		"bd dep list x-1", "bd dep tree x-1", "bd label list x-1", "bd search foo", "bd count", "bd status", "bd export", "bd update --help",
		"bd --version", "bd config get foo", "bd sync --flush-only", "cd /p && bd children x-1", "env FOO=1 bd graph x-1",
	}
	for _, c := range reads {
		if v := BeadsWriteVerb(c); v != "" {
			t.Errorf("%q is a read, refused as %q", c, v)
		}
	}
	writes := map[string]string{
		"bd batch": "batch", "bd sql 'delete from issues'": "sql", "bd comment x-1 hi": "comment", "bd note x-1 hi": "note", "bd tag x-1 foo": "tag",
		"bd priority x-1 0": "priority", "bd link a b": "link", "bd gate create": "gate", "bd supersede a b": "supersede", "bd duplicate a b": "duplicate",
		"bd dep a --blocks b": "dep", "bd dep relate a b": "dep", "bd label propagate x": "label", "bd serve": "serve", "bd dolt sql": "dolt",
		"bd close x-1": "close", "bd comments add x-1 hi": "comments", "bd config set a b": "config", "bd sync": "sync",
		"bash -c 'bd batch < f'": "batch", "xargs bd tag": "tag",
	}
	for c, want := range writes {
		if v := BeadsWriteVerb(c); v != want {
			t.Errorf("%q = %q, want %q", c, v, want)
		}
	}
}
