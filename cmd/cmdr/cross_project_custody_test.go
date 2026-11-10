package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/holding"
)

type custodyTestKey struct{}
type crossCustodyClient struct {
	*beads.MemClient
	readErr error
	foreign bool
	writes  int
	checks  int
}

func (c *crossCustodyClient) ReadCustodySnapshot(ctx context.Context, _ string, read func(context.Context) error) error {
	if ctx.Value(custodyTestKey{}) != nil {
		return read(ctx)
	}
	return read(context.WithValue(ctx, custodyTestKey{}, true))
}
func (c *crossCustodyClient) RequireLocalCustody(_ context.Context, _ string, ids []string) error {
	c.checks++
	if c.foreign {
		return &beads.CrossProjectError{Operation: "receipt", Bead: strings.Join(ids, ","), Err: errors.New("foreign source; owning Manager must establish local custody")}
	}
	return nil
}
func (c *crossCustodyClient) List(ctx context.Context, dir string, o beads.ListOptions) ([]beads.Bead, error) {
	if c.readErr != nil {
		return nil, c.readErr
	}
	return c.MemClient.List(ctx, dir, o)
}
func (c *crossCustodyClient) SetLabel(ctx context.Context, dir, id, label string, on bool) error {
	c.writes++
	return c.MemClient.SetLabel(ctx, dir, id, label, on)
}
func (c *crossCustodyClient) AddComment(ctx context.Context, dir, id, text string) error {
	c.writes++
	return c.MemClient.AddComment(ctx, dir, id, text)
}
func crossCustodyWorld() *crossCustodyClient {
	c := &crossCustodyClient{MemClient: &beads.MemClient{EdgeParentsOnly: true}}
	c.Seed(beads.Bead{ID: "a", Status: "open"})
	c.Seed(beads.Bead{ID: "b", Status: "open"})
	c.Edges = []beads.Dep{{IssueID: "b", DependsOnID: "a", Type: "blocks"}}
	return c
}
func TestCrossProjectCustodyCLI(t *testing.T) {
	for _, role := range []string{"", "manager-demo"} {
		t.Run("role="+role, func(t *testing.T) {
			t.Setenv("COMMANDER_ROLE", role)
			c := crossCustodyWorld()
			cause := errors.New("duplicate ID")
			c.readErr = fmt.Errorf("wrapped: %w", &beads.CrossProjectError{Operation: "read", Bead: "a", Err: cause})
			_, err := requireHeldAfter(context.Background(), c, "", "a", "close", "hold first", map[string]holding.Edit{"a": func(b *holding.Bead) { b.Status = "closed" }})
			if !errors.Is(err, cause) || c.writes != 0 {
				t.Fatalf("typed error lost or wrote: %v %d", err, c.writes)
			}
			c.readErr = cause
			if _, err = requireHeldAfter(context.Background(), c, "", "a", "close", "hold first", nil); err != nil {
				t.Fatalf("legacy local failure changed: %v", err)
			}
		})
	}
}
func TestCrossProjectCustodyForeignReceipt(t *testing.T) {
	t.Setenv("COMMANDER_ROLE", "")
	c := crossCustodyWorld()
	c.foreign = true
	old, recorded := leaveUnheldReason, leaveUnheldRecorded
	leaveUnheldReason, leaveUnheldRecorded = "the user wants to take it by hand", false
	t.Cleanup(func() { leaveUnheldReason, leaveUnheldRecorded = old, recorded })
	_, err := requireHeldAfter(context.Background(), c, "", "a", "close", "hold first", map[string]holding.Edit{"a": func(b *holding.Bead) { b.Status = "closed" }})
	if !beads.IsCrossProjectError(err) || c.writes != 0 || leaveUnheldRecorded {
		t.Fatalf("foreign receipt must refuse before comment/write: %v writes=%d", err, c.writes)
	}
	// The entire set is checked before any receipt is queued, including direct
	// callers and provenance changes between the preflight and settlement.
	for _, settle := range []bool{false, true} {
		c.checks = 0
		if settle {
			_, err = (custodyOwed{bc: c, ids: []string{"a", "b"}}).settle(context.Background(), "")
		} else {
			_, err = holdForManager(context.Background(), c, "", []string{"a", "b"}, "", time.Now())
		}
		if err == nil || c.writes != 0 || c.checks != 1 {
			t.Fatalf("foreign set wrote: %v writes=%d checks=%d", err, c.writes, c.checks)
		}
	}
}
