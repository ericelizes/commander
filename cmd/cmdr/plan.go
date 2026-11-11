package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// Plan edits go through the same guarded API as all existing write commands.
// A JSON actor is intentionally unsupported; identity comes from the session.
func runPlan(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "get" {
		return readPlanProposal(args[1:], stdout, stderr)
	}
	if len(args) == 0 || (args[0] != "apply" && args[0] != "propose") {
		fmt.Fprintln(stderr, "usage: cmdr plan apply|propose --file PATH (or - for stdin); cmdr plan get --id ID")
		return 2
	}
	fs := flag.NewFlagSet("plan apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "-", "JSON batch")
	proposal := fs.String("proposal", "", "apply a saved coordinated proposal")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		return 2
	}
	var input io.Reader = os.Stdin
	if *proposal != "" {
		if args[0] != "apply" || *file != "-" {
			fmt.Fprintln(stderr, "--proposal applies a saved plan without --file")
			return 2
		}
		raw, e := loadPlanProposal(*proposal)
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
		input = bytes.NewReader(raw)
	}
	if *file != "-" {
		f, e := os.Open(*file)
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
		defer f.Close()
		input = f
	}
	var request beads.PlanDocument
	d := json.NewDecoder(io.LimitReader(input, 4<<20))
	d.DisallowUnknownFields()
	if e := d.Decode(&request); e != nil {
		fmt.Fprintln(stderr, "invalid plan:", e)
		return 2
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		fmt.Fprintln(stderr, "plan must contain exactly one JSON object")
		return 2
	}
	if *proposal != "" {
		request.Basis.CoordinationID = *proposal
	}
	if request.Dir == "" {
		fmt.Fprintln(stderr, "plan requires dir")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var result beads.DoorResponse
	var e error
	if request.Publication != nil {
		if len(request.Ops) > 0 || request.Intent != nil {
			fmt.Fprintln(stderr, "publication must be a separate typed action without ops or holding intent")
			return 2
		}
		action := request.Publication
		result, e = beads.SubmitPublication(ctx, beads.PublicationRequest{Dir: request.Dir, Basis: request.Basis, Gate: action.Gate, Parent: action.Parent, Waiters: action.Waiters, Reason: action.Reason, Propose: args[0] == "propose"})
	} else if args[0] == "propose" {
		result, e = beads.SubmitPlanProposal(ctx, request.Dir, request.Ops, request.Basis, request.Intent)
	} else {
		result, e = beads.ApplyPlan(ctx, request.Dir, request.Ops, request.Basis, request.Intent)
	}
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	if e = json.NewEncoder(stdout).Encode(result); e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	return result.Exit
}

func readPlanProposal(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plan get", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("id", "", "proposal/question ID")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *id == "" {
		return 2
	}
	path, e := stateDBPath()
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	st, e := store.Open(path)
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	defer st.Close()
	if !st.IsPostgres() {
		fmt.Fprintln(stderr, "proposals require PostgreSQL")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var raw []byte
	if e = st.DB().QueryRowContext(ctx, `SELECT payload FROM commander_plan_proposals WHERE id=?`, *id).Scan(&raw); e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	question, e := st.Question(ctx, *id)
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	if e = json.NewEncoder(stdout).Encode(map[string]any{"plan": json.RawMessage(raw), "question": question}); e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	return 0
}

func loadPlanProposal(id string) ([]byte, error) {
	path, e := stateDBPath()
	if e != nil {
		return nil, e
	}
	st, e := store.Open(path)
	if e != nil {
		return nil, e
	}
	defer st.Close()
	if !st.IsPostgres() {
		return nil, fmt.Errorf("proposals require PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var raw []byte
	e = st.DB().QueryRowContext(ctx, `SELECT payload FROM commander_plan_proposals WHERE id=?`, id).Scan(&raw)
	return raw, e
}
