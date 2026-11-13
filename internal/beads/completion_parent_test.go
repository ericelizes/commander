package beads

import (
	"context"
	"github.com/example-user/commander/internal/store"
	"strings"
	"testing"
	"time"
)

// Hold the graph lock across the competing mutation. Close must wait, then
// judge the committed state rather than a pre-transaction completion read.
func TestPostgresCloseConcurrentParentMutation(t *testing.T) {
	for _, review := range []bool{false, true} {
		name := "child"
		if review {
			name = "review"
		}
		t.Run(name, func(t *testing.T) {
			c, dir, st := postgresBeads(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			root, err := c.Create(ctx, dir, "Parent", CreateOptions{Silent: true})
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			mutation := make(chan error, 1)
			go func() {
				mutation <- st.WithTx(ctx, func(q store.Querier) error {
					pid, e := projectID(ctx, q, dir)
					if e != nil {
						return e
					}
					if _, e = q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, pid); e != nil {
						return e
					}
					var args []string
					if review {
						args = []string{"update", root, "--add-label", (Wait{Kind: WaitReview, Ref: "reviewer-a", Since: time.Now()}).Label()}
					} else {
						args = []string{"create", "Concurrent child", "--parent", root}
					}
					if _, e = pgWrite(ctx, q, pid, args); e != nil {
						return e
					}
					close(entered)
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
			}()
			select {
			case <-entered:
			case e := <-mutation:
				t.Fatalf("mutation: %v", e)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			done := make(chan error, 1)
			go func() { done <- c.Close(ctx, dir, root) }()
			select {
			case e := <-done:
				close(release)
				t.Fatalf("close bypassed graph lock: %v", e)
			case <-time.After(100 * time.Millisecond):
			}
			close(release)
			if e := <-mutation; e != nil {
				t.Fatal(e)
			}
			e := <-done
			want := "unfinished children"
			if review {
				want = "Waiting for review"
			}
			if e == nil || !strings.Contains(e.Error(), want) {
				t.Fatalf("close = %v, want %s", e, want)
			}
			b, e := c.Show(ctx, dir, root)
			if e != nil || b.Status != "open" {
				t.Fatalf("parent changed: %+v %v", b, e)
			}
		})
	}
}

func TestPostgresCloseComposesWithConfirmDone(t *testing.T) {
	c, dir, _ := postgresBeads(t)
	ctx := context.Background()
	parent, e := c.Create(ctx, dir, "Outcome", CreateOptions{Labels: []string{"kind:objective", "kind:workstream"}, Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	confirm, e := c.Create(ctx, dir, "Confirm done", CreateOptions{Parent: parent, Labels: []string{"kind:objective", GateLabel, "approver:user", ClosingGateLabel}, Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Close(ctx, dir, parent); e == nil {
		t.Fatal("open Confirm done was bypassed")
	}
	if e = c.Close(ctx, dir, confirm); e != nil {
		t.Fatal(e)
	}
	if e = c.Close(ctx, dir, parent); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Create(ctx, dir, "Late child", CreateOptions{Parent: parent, Silent: true}); e == nil {
		t.Fatal("child created after parent closed")
	}
	wait := (Wait{Kind: WaitReview, Ref: "reviewer-a", Since: time.Now()}).Label()
	if _, _, e = postgresRun(ctx, dir, []string{"update", parent, "--add-label", wait}); e == nil {
		t.Fatal("review wait added after parent closed")
	}
	fresh, e := c.Create(ctx, dir, "Fresh outcome", CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = postgresRun(ctx, dir, []string{"update", fresh, "--status", "closed", "--add-label", wait}); e == nil {
		t.Fatal("same-write review wait bypassed close check")
	}
}
