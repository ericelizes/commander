package daemon

import (
	"context"
	"log"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
)

// closingSweepEvery is how often cmdrd checks every open workstream's Confirm
// done step.
const closingSweepEvery = time.Minute

// closingJob is a workstream missing its Confirm done step. Once everything
// else in it is finished, the sidebar shows Complete on it (the user's click).
type closingJob struct {
	project, dir, workstream, title string
}

// closingJobs: every open workstream, top level or nested, has one Confirm done
// step.
func closingJobs(s livedata.Snapshot) []closingJob {
	dirs := map[string]string{}
	for _, p := range s.Projects {
		dirs[p.Name] = p.Path
	}
	var out []closingJob
	var visit func(project, id, title string, steps []livedata.SnapStep)
	visit = func(project, id, title string, steps []livedata.SnapStep) {
		hasGate := false
		for i := range steps {
			st := &steps[i]
			if st.ClosingGate {
				hasGate = true
				continue
			}
			if st.Track && st.Status != "closed" && st.Status != "tombstone" && !st.Withdrawn && st.Status != beads.StatusDeferred {
				visit(project, st.BeadID, st.Title, st.Children)
			}
		}
		if !hasGate {
			out = append(out, closingJob{project: project, dir: dirs[project], workstream: id, title: title})
		}
	}
	for _, ws := range s.Workstreams {
		if ws.Status == "closed" || ws.Status == beads.StatusDeferred || dirs[ws.Project] == "" {
			continue
		}
		visit(ws.Project, ws.BeadID, ws.Title, ws.Steps)
	}
	return out
}

func (d *Daemon) runClosingSweep(ctx context.Context) {
	t := time.NewTicker(closingSweepEvery)
	defer t.Stop()
	bc := beads.New()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		snap := d.live.Snapshot(ctx)
		if snap.Degraded != nil || snap.OrchestrationMode != "active" {
			continue
		}
		for _, j := range closingJobs(snap) {
			if _, err := bc.Create(ctx, j.dir, "Confirm done", beads.CreateOptions{
				Parent:             j.workstream,
				Labels:             []string{"kind:objective", beads.GateLabel, "approver:user", beads.ClosingGateLabel},
				AcceptanceCriteria: "You confirm this workstream is done",
				Silent:             true,
			}); err != nil {
				log.Printf("closing: could not add Confirm done to %s %s: %v", j.project, j.workstream, err)
			}
		}
	}
}
