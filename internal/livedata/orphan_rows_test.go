package livedata

import "testing"

func TestDropOrphansTakesAShelvedSubtreeOffTheBoard(t *testing.T) {
	rows := []ObjectiveRow{
		{BeadID: "p-a", Status: "open"},
		{BeadID: "p-a.1", ParentBeadID: "p-a", Status: "open"},
		{BeadID: "p-b.2.1", ParentBeadID: "p-b.2", Status: "closed"}, // closed under an unlisted parent
		{BeadID: "p-b.8.3", ParentBeadID: "p-b.8", Status: "open"},   // open, but workstream p-b is closed
		{BeadID: "p-c.1", ParentBeadID: "p-c", Status: "open"},       // parent never listed: stays
	}
	got := dropOrphans(rows, map[string]bool{"p-b": true})
	var ids []string
	for _, r := range got {
		ids = append(ids, r.BeadID)
	}
	if len(ids) != 3 || ids[0] != "p-a" || ids[1] != "p-a.1" || ids[2] != "p-c.1" {
		t.Fatalf("rows = %v, want p-a, p-a.1, p-c.1", ids)
	}
}
