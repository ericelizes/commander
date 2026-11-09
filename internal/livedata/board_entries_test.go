package livedata

import "testing"

func TestBoardEntries_LeaveCommanderOutAndKeyWithoutAges(t *testing.T) {
	d := Display{
		Commander: BoardCommander{Kind: "working", Text: "working"},
		Workstreams: []BoardRow{{Key: "p:a", N: 1, Title: "Alpha", BeadID: "a", ManagerRole: "manager-a", Kind: "stalled", Text: "Stalled", Since: 100,
			Tracks: []BoardTrack{{Key: "t", BeadID: "a.1", Letter: "B", Title: "Two", Kind: "working", Text: "Working"}}}},
		Asks: []BoardAsk{{Key: "ask:p:a.2", Text: "Approve", Rank: 1, Workstream: "Alpha", WsKey: "p:a"}},
	}
	es := BoardEntries(d, 400, "commander")
	if len(es) != 3 || es[0].ID != "1" || es[1].ID != "1B" || !es[1].Track || !es[2].Ask {
		t.Fatalf("entries %+v", es)
	}
	if es[0].Line() != "#1 Alpha: Stalled (100% done, no gates, for 5m)" || es[0].Key != "Alpha: Stalled (100% done, no gates)" {
		t.Fatalf("row %q key %q", es[0].Line(), es[0].Key)
	}
	if es[2].Line() != "ask #1 Approve (Alpha)" {
		t.Fatalf("ask %q", es[2].Line())
	}
	if got := BoardEntries(d, 400, "manager-zz"); len(got) != 0 {
		t.Fatalf("another Manager's board: %+v", got)
	}
}
