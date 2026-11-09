package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/example-user/commander/internal/tui"
)

// runInternalWorkstreams prints the Workstreams column's numbered rows as JSON
// so Commander can resolve "work on 3" to a project and bead.
func runInternalWorkstreams(stdout, stderr *os.File) int {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rows, err := tui.ListNumberedWorkstreams(ctx)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(rows); err != nil {
		emitErr(stderr, fmt.Sprintf("encode result: %v", err))
		return 1
	}
	return 0
}
