package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/example-user/commander/internal/store"
	"os"
	"time"
)

func runSupervision(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: cmdr supervision status|acknowledge --project-id ID --bead ID [--epoch N]")
		return 2
	}
	fs := flag.NewFlagSet("supervision", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pid := fs.Int64("project-id", 0, "")
	bead := fs.String("bead", "", "")
	epoch := fs.Int64("epoch", 0, "")
	request := fs.String("request", "", "")
	if fs.Parse(args[1:]) != nil || *pid <= 0 || *bead == "" {
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
		fmt.Fprintln(stderr, "durable supervision requires PostgreSQL")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	switch args[0] {
	case "acknowledge":
		e = st.AcknowledgeSupervision(ctx, *pid, *bead, *epoch, os.Getenv("COMMANDER_ROLE"), *request)
		if e == nil {
			json.NewEncoder(stdout).Encode(map[string]bool{"acknowledged": true})
		}
	case "status":
		var result map[string]any
		var raw []byte
		e = st.DB().QueryRowContext(ctx, `SELECT to_jsonb(x) FROM bead_supervision x WHERE project_id=? AND bead_id=?`, *pid, *bead).Scan(&raw)
		if e == nil {
			e = json.Unmarshal(raw, &result)
			if e == nil {
				delete(result, "lease_token")
				json.NewEncoder(stdout).Encode(result)
			}
		}
	default:
		fmt.Fprintln(stderr, "unknown supervision command")
		return 2
	}
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	return 0
}
