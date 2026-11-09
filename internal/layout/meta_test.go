package layout

import (
	"os"
	"strings"
	"testing"
)

func TestWindowMeta_ShortensHomeAndDropsTheProvider(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	meta := WindowMeta("manager", home+"/code/app")
	if !strings.HasPrefix(meta, "~/code/app") {
		t.Fatalf("meta = %q, want a ~-relative cwd first", meta)
	}
	if strings.Contains(strings.TrimPrefix(meta, "~/code/app"), "/") {
		t.Fatalf("meta = %q, model must not carry a provider prefix", meta)
	}
}

func TestWindowMeta_NoDanglingSeparatorWithoutCwd(t *testing.T) {
	if got := WindowMeta("operator", ""); strings.HasPrefix(got, " ·") {
		t.Fatalf("empty cwd leaves a dangling separator: %q", got)
	}
}
