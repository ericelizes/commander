package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
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
	if len(args) > 0 && args[0] == "check" {
		return runPlanCheck(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "template" {
		return runPlanTemplate(args[1:], stdout, stderr)
	}
	if len(args) == 0 || (args[0] != "apply" && args[0] != "propose") {
		fmt.Fprintln(stderr, "usage: cmdr plan apply|propose --file PATH (or - for stdin): {\"project\": NAME, \"edits\": [{\"op\": create|retitle|set_owner|set_criteria|move|link|unlink|supersede|close, ...}]} (or dir and raw ops); cmdr plan get --id ID; cmdr plan check [--dir PROJECT_DIR] [--workstream ID]... [--json]; cmdr plan template NAME --parent ID --subject TEXT [--owner-go]")
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if request.Dir == "" && request.Project != "" {
		dir, e := beads.ProjectDir(ctx, request.Project)
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
		request.Dir = dir
	}
	if request.Dir == "" {
		fmt.Fprintln(stderr, "plan requires dir or project")
		return 2
	}
	// Tree edits compile to ops and ride in the same atomic batch.
	if len(request.Edits) > 0 {
		if request.Publication != nil {
			fmt.Fprintln(stderr, "publication must be a separate typed action without edits")
			return 2
		}
		ops, e := beads.CompileTreeEdits(request.Edits)
		if e != nil {
			fmt.Fprintln(stderr, "invalid edit:", e)
			return 2
		}
		request.Ops, request.Edits = append(request.Ops, ops...), nil
	}
	// A batch sent without the versions it was read at is checked against the tree now.
	if request.Publication == nil && request.Basis.JurisdictionRevision == 0 && len(request.Basis.Beads) == 0 && len(request.Ops) > 0 {
		basis, e := beads.CurrentPlanBasis(ctx, request.Dir, request.Ops)
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
		basis.RequestID, basis.CoordinationID = request.Basis.RequestID, request.Basis.CoordinationID
		request.Basis = basis
	}
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

type planCheckRoots []string

func (r *planCheckRoots) String() string     { return strings.Join(*r, ",") }
func (r *planCheckRoots) Set(v string) error { *r = append(*r, v); return nil }

// runPlanCheck reports a project's tree shape problems read-only: the same
// rules `cmdr plan apply` refuses, so a tree can be cleaned up before re-planning.
func runPlanCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plan check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", ".", "project directory")
	asJSON := fs.Bool("json", false, "print JSON")
	var roots planCheckRoots
	fs.Var(&roots, "workstream", "workstream bead id (repeatable; default: every open workstream)")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	vs, e := beads.PlanShapeReport(ctx, *dir, roots)
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	if *asJSON {
		if vs == nil {
			vs = []beads.ShapeViolation{}
		}
		if e = json.NewEncoder(stdout).Encode(vs); e != nil {
			return 1
		}
	} else {
		if len(vs) == 0 {
			fmt.Fprintln(stdout, "tree shape: no problems")
		}
		for _, v := range vs {
			fmt.Fprintln(stdout, "- "+v.String())
		}
	}
	if len(vs) > 0 {
		return 3
	}
	return 0
}

// runPlanTemplate prints a template's plan-apply operations under a parent:
// the recurring shapes (pr-babysit, app-release, investigation, teach-agents) built so the
// shape guard accepts them. Merge the ops into a plan apply request.
func runPlanTemplate(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, "usage: cmdr plan template <name> --parent ID --subject TEXT [--owner-go]")
		for _, n := range beads.PlanTemplateNames() {
			fmt.Fprintf(stderr, "  %-22s %s\n", n, beads.PlanTemplateTitles[n])
		}
		return 2
	}
	name := args[0]
	fs := flag.NewFlagSet("plan template", flag.ContinueOnError)
	fs.SetOutput(stderr)
	parent := fs.String("parent", "", "the track or step the steps go under")
	subject := fs.String("subject", "", "what the steps are about, e.g. \"notifications redesign\" (no article)")
	ownerGo := fs.Bool("owner-go", false, "app-release: add the owner's written go before the deploy (off by default)")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		return 2
	}
	ops, e := beads.PlanTemplateOps(name, *parent, *subject, *ownerGo)
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 2
	}
	if e = json.NewEncoder(stdout).Encode(map[string]any{"template": name, "title": beads.PlanTemplateTitles[name], "ops": ops}); e != nil {
		return 1
	}
	return 0
}
