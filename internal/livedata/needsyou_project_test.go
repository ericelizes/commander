package livedata

import (
	"testing"

	"github.com/example-user/commander/internal/store"
)

func TestProjectOfBead(t *testing.T) {
	ps := []store.Project{{Name: "commander"}, {Name: "commander-web"}, {Name: "other"}}
	for id, want := range map[string]string{
		"commander-il9.91": "commander",
		"commander-web-a1": "commander-web",
		"other-9":          "other",
		"unknown-1":        "",
		"commanderx-1":     "",
	} {
		if got := ProjectOfBead(ps, id); got != want {
			t.Errorf("%s: got %q want %q", id, got, want)
		}
	}
}

func TestFilterProject(t *testing.T) {
	ps := []store.Project{{Name: "a"}, {Name: "b"}}
	if got := filterProject(ps, "b"); len(got) != 1 || got[0].Name != "b" {
		t.Fatalf("got %v", got)
	}
}
