package daemon

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/example-user/commander/internal/api"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
)

// closingSweepEvery is how often cmdrd checks every open workstream's Confirm
// done step.
const closingSweepEvery = time.Minute

// closingJob is one write the sweep owes: create a workstream's Confirm done
// step, or (card) ask the user on it once everything else is finished.
type closingJob struct {
	project, dir, workstream, title, gate string
	card                                  bool
}

// closingJobs: every open workstream, top level or nested, has one Confirm done
// step; once everything else in it is finished, that step asks the user.
func closingJobs(s livedata.Snapshot) []closingJob {
	dirs := map[string]string{}
	for _, p := range s.Projects {
		dirs[p.Name] = p.Path
	}
	var out []closingJob
	var visit func(project, id, title string, steps []livedata.SnapStep)
	visit = func(project, id, title string, steps []livedata.SnapStep) {
		var gate *livedata.SnapStep
		others, open := 0, 0
		for i := range steps {
			st := &steps[i]
			if st.ClosingGate {
				if gate == nil || st.Status != "closed" {
					gate = st
				}
				continue
			}
			others++
			if st.Status != "closed" && st.Status != "tombstone" {
				open++
				if st.Track && !st.Withdrawn && st.Status != beads.StatusDeferred {
					visit(project, st.BeadID, st.Title, st.Children)
				}
			}
		}
		switch {
		case gate == nil:
			out = append(out, closingJob{project: project, dir: dirs[project], workstream: id, title: title})
		case gate.Status != "closed" && others > 0 && open == 0 && gate.State != beads.StateNeedsUser && gate.Decision == nil:
			out = append(out, closingJob{project: project, dir: dirs[project], workstream: id, title: title, gate: gate.BeadID, card: true})
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

// closingCard is the "Is it done?" card on a Confirm done step.
func closingCard(title string) string {
	raw, _ := json.Marshal(store.Decision{
		Question: "Is “" + title + "” done?",
		Context:  "Everything in this workstream is finished. Confirming closes it; Not yet sends it back to Commander.",
		Options: []store.DecisionOption{
			{Label: "Done, close it", Recommended: true, Approves: true},
			{Label: "Not yet", Implications: "Commander adds the missing work"},
		},
	})
	return string(raw)
}

func (d *Daemon) runClosingSweep(ctx context.Context) {
	t := time.NewTicker(closingSweepEvery)
	defer t.Stop()
	run := api.ExecRunner(cmdrPath())
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
			if j.card {
				if res := run(ctx, []string{"internal-set-objective", "--project", j.project, "--bead", j.gate, "--state", "needs-user", "--decision", closingCard(j.title)}); !res.OK {
					log.Printf("closing: could not ask about %s %s: %s", j.project, j.workstream, res.Stderr)
				}
				continue
			}
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
