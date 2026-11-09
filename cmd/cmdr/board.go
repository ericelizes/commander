package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/example-user/commander/internal/livedata"
)

// runBoard is `cmdr board [--role <role>] [--json] [--snapshot <file>]`: the
// board exactly as the user's sidebar shows it. Commander (the default) gets
// every row; a manager-<bead> role gets only its workstream. `--workflow <n|bead>`
// prints one workstream's (or track's) workflow, as the app's Workflow view shows it;
// `--diagram <n|bead>` its diagram (lanes: you, agents, elsewhere), as the app draws it.
func runBoard(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("board", flag.ContinueOnError)
	fs.SetOutput(stderr)
	role := fs.String("role", "commander", "whose view: commander, or manager-<bead> for one workstream")
	asJSON := fs.Bool("json", false, "print the display block as JSON")
	file := fs.String("snapshot", "", "read this snapshot JSON file instead of the live one from cmdrd")
	workflow := fs.String("workflow", "", "print the workflow of workstream <n> or of a workstream or track bead")
	diagram := fs.String("diagram", "", "print the diagram of workstream <n> or of a workstream or track bead: each step's lane (you, agents, elsewhere), gates, criteria, findings")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	body, err := loadSnapshot(appCommanderHome(), *file)
	if err != nil {
		fmt.Fprintln(stderr, "board:", err)
		return 2
	}
	var snap livedata.Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		fmt.Fprintln(stderr, "board: snapshot: decode:", err)
		return 2
	}
	livedata.LoadBoardZone(appCommanderHome())
	livedata.LoadTicketPrefix(appCommanderHome())
	now := time.Now().Unix()
	d := boardOf(snap, now)
	if *diagram != "" {
		dg, err := diagramOf(snap, d, now, *diagram)
		if err != nil {
			fmt.Fprintln(stderr, "board:", err)
			return 2
		}
		if *asJSON {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(dg)
			return 0
		}
		for _, l := range livedata.DiagramLines(dg) {
			fmt.Fprintln(stdout, l)
		}
		return 0
	}
	if *workflow != "" {
		w, err := workflowOf(snap, d, now, *workflow)
		if err != nil {
			fmt.Fprintln(stderr, "board:", err)
			return 2
		}
		if *asJSON {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(w)
			return 0
		}
		for _, l := range livedata.WorkflowLines(w) {
			fmt.Fprintln(stdout, l)
		}
		return 0
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(d)
		return 0
	}
	for _, l := range livedata.BoardLines(d, now, *role) {
		fmt.Fprintln(stdout, l)
	}
	return 0
}

// boardOf is the snapshot's display block, derived when cmdrd predates it.
func boardOf(s livedata.Snapshot, now int64) livedata.Display {
	if s.Display != nil {
		return *s.Display
	}
	return livedata.Board(s, now)
}

// workflowOf finds the workflow for a row number, workstream bead or track
// bead. A cmdrd that predates workflows sends none: derive it from the same snapshot.
func workflowOf(s livedata.Snapshot, d livedata.Display, now int64, which string) (*livedata.Workflow, error) {
	if !slices.ContainsFunc(d.Workstreams, func(r livedata.BoardRow) bool { return r.Workflow != nil }) {
		d = livedata.Board(s, now)
	}
	for _, r := range d.Workstreams {
		if strconv.Itoa(r.N) == which || r.BeadID == which {
			return r.Workflow, nil
		}
		if w := r.Workflow.Sub(which); w != nil {
			return w, nil
		}
	}
	return nil, fmt.Errorf("no workstream or track %q on the board", which)
}

// diagramOf finds the diagram for a row number, workstream bead or track bead,
// derived from the same snapshot when cmdrd predates diagrams.
func diagramOf(s livedata.Snapshot, d livedata.Display, now int64, which string) (*livedata.Diagram, error) {
	if !slices.ContainsFunc(d.Workstreams, func(r livedata.BoardRow) bool { return r.Diagram != nil }) {
		d = livedata.Board(s, now)
	}
	for _, r := range d.Workstreams {
		if strconv.Itoa(r.N) == which || r.BeadID == which {
			if r.Diagram == nil {
				return nil, fmt.Errorf("workstream %q has no steps", which)
			}
			return r.Diagram, nil
		}
		if dg := r.Diagram.Sub(which); dg != nil {
			return dg, nil
		}
	}
	return nil, fmt.Errorf("no workstream or open track %q on the board", which)
}
