package tui

import "testing"

// A NEEDS YOU ask under a promoted workstream opens that workstream's Manager.
func TestWsmgrRootFollowsLoadedParents(t *testing.T) {
	c := &ObjectivesColumn{live: CommanderLiveData{Objectives: []ObjectiveRow{
		{BeadID: "example-app-t02"}, {BeadID: "example-app-t02.2", ParentBeadID: "example-app-t02"},
		{BeadID: "example-app-t02.8"}, {BeadID: "example-app-t02.8.4", ParentBeadID: "example-app-t02.8"},
	}}}
	for in, want := range map[string]string{"example-app-t02.8.4": "example-app-t02.8", "example-app-t02.2": "example-app-t02"} {
		if got := c.wsmgrRoot(in); got != want {
			t.Errorf("wsmgrRoot(%q) = %q, want %q", in, got, want)
		}
	}
}
