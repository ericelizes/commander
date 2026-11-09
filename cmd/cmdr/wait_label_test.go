package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
)

// A full sha does not fit a label: the refusal names the form that does.
func TestWaitingOnAFullShaIsToldToUseAShortOne(t *testing.T) {
	wt, err := beads.ParseWait("main-ci:349a7f4e1f0c2b6d8a9e3c5b7d1f2a4c6e8b0d13")
	if err != nil {
		t.Fatal(err)
	}
	err = applyWaiting(context.Background(), &beads.MockClient{}, "/p", "p-ws.1", []beads.Wait{wt}, time.Unix(1793904844, 0))
	if err == nil || !strings.Contains(err.Error(), "use a short sha") {
		t.Fatalf("err = %v", err)
	}
}
