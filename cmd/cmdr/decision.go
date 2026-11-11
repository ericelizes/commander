package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/api"
	"github.com/example-user/commander/internal/store"
)

func runDecision(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: cmdr decision inspect --project-id ID --bead ID; cmdr decision reconcile --file PATH")
		return 2
	}
	fs := flag.NewFlagSet("decision", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pid := fs.Int64("project-id", 0, "")
	bead := fs.String("bead", "", "")
	file := fs.String("file", "-", "")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if args[0] == "inspect" {
		if *pid <= 0 || *bead == "" {
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
			fmt.Fprintln(stderr, "decision recovery requires PostgreSQL")
			return 1
		}
		var revision, scope, authority int64
		var title, description, acceptance, parent string
		var labels json.RawMessage
		if e = st.DB().QueryRowContext(ctx, `SELECT plan_revision,human_scope_epoch,human_authority_epoch,title,description,acceptance_criteria,COALESCE(parent_id,''),labels FROM bead_tasks WHERE project_id=? AND id=? AND deleted_at IS NULL`, *pid, *bead).Scan(&revision, &scope, &authority, &title, &description, &acceptance, &parent, &labels); e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
		owner, e := st.Jurisdiction(ctx, *pid, *bead)
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
		generation, e := st.NeedsGeneration(ctx, *pid, *bead)
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
		saved, e := st.GetNeedsResolution(ctx, *pid, *bead)
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
		pitches, e := st.ListPitches(ctx, *pid, *bead)
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
		exact := []store.Pitch{}
		for _, p := range pitches {
			if p.BeadID == *bead {
				exact = append(exact, p)
			}
		}
		if e = json.NewEncoder(stdout).Encode(map[string]any{"project_id": *pid, "bead_id": *bead, "plan_revision": revision, "jurisdiction_revision": owner.Revision, "commander": owner.CommanderID, "scope_epoch": scope, "authority_epoch": authority, "generation": generation, "saved_answer": saved, "pitches": exact, "work": map[string]any{"title": title, "description": description, "acceptance": acceptance, "parent": parent, "labels": labels}}); e != nil {
			return 1
		}
		return 0
	}
	if args[0] != "reconcile" {
		return 2
	}
	var input io.Reader = os.Stdin
	if *file != "-" {
		f, e := os.Open(*file)
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
		defer f.Close()
		input = f
	}
	var request struct {
		store.DecisionReconciliation
		Env []string `json:"env,omitempty"`
	}
	// Session attribution is not accepted from the input file.
	var body store.DecisionReconciliation
	d := json.NewDecoder(io.LimitReader(input, 128<<10))
	d.DisallowUnknownFields()
	if e := d.Decode(&body); e != nil {
		fmt.Fprintln(stderr, e)
		return 2
	}
	if e := d.Decode(new(any)); e != io.EOF {
		return 2
	}
	request.DecisionReconciliation = body
	for _, key := range []string{"COMMANDER_ROLE", "COMMANDER_RUN_ID", "COMMANDER_RUN_SESSION", "COMMANDER_MANAGER_ROLE"} {
		request.Env = append(request.Env, key+"="+os.Getenv(key))
	}
	home := agentrt.CommanderHome()
	base, e := os.ReadFile(filepath.Join(home, "api-url"))
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	token, e := os.ReadFile(filepath.Join(home, "api-token"))
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	raw, _ := json.Marshal(request)
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(strings.TrimSpace(string(base)), "/")+api.DecisionReconciliationPath, bytes.NewReader(raw))
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Content-Type", "application/json")
	response, e := http.DefaultClient.Do(req)
	if e != nil {
		fmt.Fprintln(stderr, "decision reconciliation unavailable; no local changes were made:", e)
		return 1
	}
	defer response.Body.Close()
	result, e := io.ReadAll(io.LimitReader(response.Body, 128<<10))
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	if response.StatusCode != http.StatusOK {
		fmt.Fprintln(stderr, string(result))
		return 1
	}
	_, e = stdout.Write(result)
	if e != nil {
		return 1
	}
	return 0
}
