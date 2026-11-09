package beads

// NestedTree is the shared fixture for nested-Manager routing tests: root R with
// nested workstreams A and B, A1 nested under A with step S, a legacy track T
// (kind:track only) with step TS, a closed nested C with step CS and a parked
// nested P with step PS, and an open nested P1 under P with step P1S (out of
// play with P: owned by the root, never registered). Ids are invented (demo-*).
const (
	NestedRoot   = "demo-r"
	NestedA      = "demo-a"
	NestedB      = "demo-b"
	NestedA1     = "demo-a1"
	NestedStep   = "demo-s"
	NestedTrack  = "demo-t"
	NestedTStep  = "demo-ts"
	NestedClosed = "demo-c"
	NestedCStep  = "demo-cs"
	NestedParked = "demo-p"
	NestedPStep  = "demo-ps"
	NestedBStep  = "demo-bs"
	NestedP1     = "demo-p1"
	NestedP1Step = "demo-p1s"
)

// NestedTree seeds a MemClient (parents by edge only, as br) with the tree.
func NestedTree() *MemClient {
	ws := []string{"kind:objective", WorkstreamLabel}
	step := []string{"kind:objective"}
	m := &MemClient{EdgeParentsOnly: true}
	for _, b := range []Bead{
		{ID: NestedRoot, Title: "Root", Labels: ws},
		{ID: NestedA, Title: "Nested A", Labels: ws},
		{ID: NestedB, Title: "Nested B", Labels: ws},
		{ID: NestedA1, Title: "Nested A1", Labels: ws},
		{ID: NestedStep, Title: "Step under A1", Labels: step},
		{ID: NestedBStep, Title: "Step under B", Labels: step},
		{ID: NestedTrack, Title: "Legacy track", Labels: []string{"kind:objective", TrackLabel}},
		{ID: NestedTStep, Title: "Step under track", Labels: step},
		{ID: NestedClosed, Title: "Closed nested", Labels: ws, Status: "closed"},
		{ID: NestedCStep, Title: "Step under closed", Labels: step},
		{ID: NestedParked, Title: "Parked nested", Labels: ws, Status: StatusDeferred},
		{ID: NestedPStep, Title: "Step under parked", Labels: step},
		{ID: NestedP1, Title: "Open nested under parked", Labels: ws},
		{ID: NestedP1Step, Title: "Step under open-under-parked", Labels: step},
	} {
		m.Seed(b)
	}
	for child, parent := range map[string]string{
		NestedA: NestedRoot, NestedB: NestedRoot, NestedA1: NestedA, NestedStep: NestedA1,
		NestedBStep: NestedB, NestedTrack: NestedRoot, NestedTStep: NestedTrack,
		NestedClosed: NestedRoot, NestedCStep: NestedClosed, NestedParked: NestedRoot, NestedPStep: NestedParked,
		NestedP1: NestedParked, NestedP1Step: NestedP1,
	} {
		m.Edges = append(m.Edges, Dep{IssueID: child, DependsOnID: parent, Type: "parent-child"})
	}
	return m
}
