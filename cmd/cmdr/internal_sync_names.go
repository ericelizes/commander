package main

import (
	"context"
	"fmt"
	"os"
	"time"
)

// runInternalSyncWindows renames workstream Manager windows to match the
// workstreams' current ranks ("#<rank> <title>").
func runInternalSyncWindows(stdout, stderr *os.File) int {
	opener, cleanup, err := newWorkstreamOpener()
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := opener.SyncNames(ctx); err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	fmt.Fprintln(stdout, `{"synced":true}`)
	return 0
}
