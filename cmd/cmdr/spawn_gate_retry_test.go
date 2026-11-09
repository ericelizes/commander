package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

// flakyClient fails its first listFails List calls and detailFails Details
// calls with err, then answers like the wrapped client.
type flakyClient struct {
	beads.Client
	err         error
	listFails   int
	detailFails int
	lists       int
	details     int
}

func (f *flakyClient) List(ctx context.Context, dir string, o beads.ListOptions) ([]beads.Bead, error) {
	f.lists++
	if f.lists <= f.listFails {
		return nil, f.err
	}
	return f.Client.List(ctx, dir, o)
}

func (f *flakyClient) Details(ctx context.Context, dir string, ids []string) (map[string]beads.Detail, error) {
	f.details++
	if f.details <= f.detailFails {
		return nil, f.err
	}
	return f.Client.Details(ctx, dir, ids)
}

var errKilled = fmt.Errorf("br list: signal: killed: ")

func TestSpawnGateRetryOnceOnKilledRead(t *testing.T) {
	t.Setenv("COMMANDER_WORKFLOW_GATE", "enforce")
	closed := gateUnder("w1.g", "w1", "Workflow approved", "closed")
	f := &flakyClient{Client: wfTree(closed), err: errKilled, listFails: 1}
	if err := requireWorkflowGate(context.Background(), f, "/p", "w1.1", &strings.Builder{}); err != nil {
		t.Fatalf("one killed read must be retried: %v", err)
	}
	if f.lists != 2 {
		t.Fatalf("lists = %d; want 2", f.lists)
	}
}

func TestSpawnGateRetryStillFailingIsFriendlyWhenEnforced(t *testing.T) {
	t.Setenv("COMMANDER_WORKFLOW_GATE", "enforce")
	f := &flakyClient{Client: wfTree(), err: fmt.Errorf("br show: %w: ", context.DeadlineExceeded), listFails: 5}
	err := requireWorkflowGate(context.Background(), f, "/p", "w1.1", &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "could not read the workflow gate of w1.1 (busy), retry in a minute") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "deadline") {
		t.Fatalf("raw timeout text leaked: %v", err)
	}
	if f.lists != 2 {
		t.Fatalf("lists = %d; want exactly one retry", f.lists)
	}
}

func TestSpawnGateRetryWarnModeDoesNotBlockOnReadFailure(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv("COMMANDER_WORKFLOW_GATE", "")
	f := &flakyClient{Client: wfTree(), err: context.DeadlineExceeded, listFails: 5}
	var warn strings.Builder
	if err := requireWorkflowGate(context.Background(), f, "/p", "w1.1", &warn); err != nil {
		t.Fatalf("warn mode must not block: %v", err)
	}
	if !strings.Contains(warn.String(), "could not read the workflow gate") || !strings.Contains(warn.String(), "retry in a minute") {
		t.Fatalf("stderr = %q", warn.String())
	}
}

func TestSpawnGateRetryNonBusyErrorIsNotRetried(t *testing.T) {
	t.Setenv("COMMANDER_WORKFLOW_GATE", "enforce")
	f := &flakyClient{Client: wfTree(), err: errors.New("br list: parse json"), listFails: 5}
	if err := requireWorkflowGate(context.Background(), f, "/p", "w1.1", &strings.Builder{}); err == nil || f.lists != 1 {
		t.Fatalf("err=%v lists=%d; want an error after one read", err, f.lists)
	}
}

func TestSpawnGateRetryOpenGateAboveRetriesAndIsFriendly(t *testing.T) {
	f := &flakyClient{Client: gateTree("example-app-t02"), err: errKilled, detailFails: 1}
	// Lineage reads through Details too; its failure falls back to dotted ids, so
	// fail enough reads that the chain read itself is retried once.
	got, err := openGateAbove(context.Background(), f, "/p", "example-app-t02.2.1")
	if err != nil || got != "gate-a" {
		t.Fatalf("got %q, %v", got, err)
	}
	f = &flakyClient{Client: gateTree("example-app-t02"), err: errKilled, detailFails: 100}
	_, err = openGateAbove(context.Background(), f, "/p", "example-app-t02.2.1")
	if err == nil || !strings.Contains(err.Error(), "retry in a minute") || strings.Contains(err.Error(), "killed") {
		t.Fatalf("err = %v", err)
	}
}
