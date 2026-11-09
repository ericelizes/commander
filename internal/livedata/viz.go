package livedata

// A bead's level in the app's Graph: 1 shows only gates, 2 adds the stages, 3
// shows every bead. The viz:<n> label sets it; without one a gate is 1, a
// stage 2 and anything else 3.

// EffectiveViz is a step's level: its own label, else the default for what it is.
func EffectiveViz(r ObjectiveRow, stage bool) int {
	switch {
	case r.Viz > 0:
		return r.Viz
	case r.Gate:
		return 1
	case stage:
		return 2
	}
	return 3
}

// stageIDs picks the lifecycle stages among a workstream's top-level steps: the
// gates and tracks, whatever a blocks edge ties to a gate, and open steps tied to those.
// The rest (a plan, a spike, a unit) is history. Mirrors spineStageIds in the
// app's graph.ts.
func stageIDs(nodes []*StepNode) map[string]bool {
	// Match the renderer: retired gates and stages cannot anchor live work.
	active := make([]*StepNode, 0, len(nodes))
	for _, n := range nodes {
		if !n.Row.Superseded {
			active = append(active, n)
		}
	}
	nodes = active
	ids := map[string]bool{}
	for _, n := range nodes {
		ids[n.Row.BeadID] = true
	}
	blockedBy := func(n *StepNode) []string {
		var out []string
		for _, b := range n.Row.BlockedBy {
			if ids[b] {
				out = append(out, b)
			}
		}
		return out
	}
	stages := map[string]bool{}
	for _, n := range nodes {
		if n.Row.Gate || n.Row.Track {
			stages[n.Row.BeadID] = true // a track is a stage of its workstream
		}
	}
	touches := func(n *StepNode, set map[string]bool) bool {
		for _, b := range blockedBy(n) {
			if set[b] {
				return true
			}
		}
		for _, m := range nodes {
			if !set[m.Row.BeadID] {
				continue
			}
			for _, b := range blockedBy(m) {
				if b == n.Row.BeadID {
					return true
				}
			}
		}
		return false
	}
	gates := map[string]bool{}
	for id := range stages {
		gates[id] = true
	}
	for _, n := range nodes {
		if touches(n, gates) {
			stages[n.Row.BeadID] = true
		}
	}
	for grew := true; grew; {
		grew = false
		for _, n := range nodes {
			if stages[n.Row.BeadID] || n.Row.Status == "closed" || !touches(n, stages) {
				continue
			}
			stages[n.Row.BeadID] = true
			grew = true
		}
	}
	return stages
}
