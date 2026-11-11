package main

import (
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

func runExecution(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: cmdr execution status|ready|pause --project-id ID --bead ID [--revision N --jurisdiction-revision N --capabilities code,mac]")
		return 2
	}
	fs := flag.NewFlagSet("execution", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pid := fs.Int64("project-id", 0, "")
	bead := fs.String("bead", "", "")
	revision := fs.Int64("revision", 0, "")
	jrev := fs.Int64("jurisdiction-revision", 0, "")
	caps := fs.String("capabilities", "", "")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *pid <= 0 || *bead == "" {
		return 2
	}
	if args[0] != "status" && args[0] != "ready" && args[0] != "pause" {
		return 2
	}
	if args[0] != "status" && beads.HasExecutionAttribution(os.Environ()) {
		fmt.Fprintln(stderr, "execution sessions cannot change planning readiness or orchestration")
		return 1
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out any
	if args[0] == "status" {
		var raw []byte
		e = st.DB().QueryRowContext(ctx, `SELECT jsonb_build_object('project_id',project_id,'bead_id',id,'plan_revision',plan_revision,'planning_commander',commander_planning_owner(project_id,id),'jurisdiction_revision',COALESCE((SELECT revision FROM jurisdiction_projects WHERE project_id=b.project_id),1),'ready',execution_ready,'capabilities',execution_capabilities,'attempts',execution_attempts,'retry_at',execution_due_at,'error',execution_error,'active_run_id',active_run_id) FROM bead_tasks b WHERE project_id=? AND id=? AND deleted_at IS NULL`, *pid, *bead).Scan(&raw)
		if e == nil {
			e = json.Unmarshal(raw, &out)
		}
	} else {
		actor := os.Getenv("COMMANDER_ROLE")
		if actor == "" && os.Getenv("COMMANDER_MANAGER_ROLE") != "" {
			fmt.Fprintln(stderr, "executor cannot change planning readiness")
			return 1
		}
		capabilities := []string{}
		if *caps != "" {
			capabilities = strings.Split(*caps, ",")
		}
		var next int64
		next, e = st.SetExecutionReady(ctx, *pid, *bead, actor, *revision, *jrev, args[0] == "ready", capabilities)
		out = map[string]any{"plan_revision": next, "ready": args[0] == "ready"}
	}
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	if e = json.NewEncoder(stdout).Encode(out); e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	return 0
}

func runOrchestration(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: cmdr orchestration status|set --revision N --mode shadow|active|off --reason TEXT")
		return 2
	}
	fs := flag.NewFlagSet("orchestration", flag.ContinueOnError)
	fs.SetOutput(stderr)
	revision := fs.Int64("revision", 0, "")
	mode := fs.String("mode", "", "")
	reason := fs.String("reason", "", "")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		return 2
	}
	if args[0] != "status" && args[0] != "set" {
		return 2
	}
	if args[0] != "status" && beads.HasExecutionAttribution(os.Environ()) {
		fmt.Fprintln(stderr, "execution sessions cannot change planning readiness or orchestration")
		return 1
	}
	if args[0] == "set" && (os.Getenv("COMMANDER_ROLE") != "" || os.Getenv("COMMANDER_MANAGER_ROLE") != "") {
		fmt.Fprintln(stderr, "installation orchestration changes require the user interface")
		return 1
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var control store.OrchestrationControl
	if args[0] == "status" {
		control, e = st.OrchestrationControl(ctx)
	} else {
		control, e = st.SetOrchestrationControl(ctx, *revision, *mode, *reason)
	}
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	json.NewEncoder(stdout).Encode(map[string]any{"control": control, "restart_required": args[0] == "set"})
	return 0
}
