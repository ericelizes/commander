package beads

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type missingBlockerClient struct {
	*MemClient
	err error
}

func (c *missingBlockerClient) Show(context.Context, string, string) (*Bead, error) {
	return nil, c.err
}

func TestBehindOpenBlockerTypedNotFound(t *testing.T) {
	for _, typed := range []bool{false, true} {
		t.Run(fmt.Sprint(typed), func(t *testing.T) {
			c := &missingBlockerClient{MemClient: &MemClient{}, err: ErrNotFound}
			c.Seed(Bead{ID: "step", Status: "open"})
			c.Edges = []Dep{{IssueID: "step", DependsOnID: "prerequisite", Type: "blocks"}}
			if typed {
				c.err = fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", &CrossProjectError{Operation: "read", Bead: "prerequisite", Err: ErrNotFound}))
			}
			behind, err := BehindOpenBlocker(context.Background(), c, "", "step", "")
			if behind {
				t.Fatal("missing blocker holds step")
			}
			if typed {
				if !IsCrossProjectError(err) || !errors.Is(err, ErrNotFound) {
					t.Fatalf("typed cause lost: %v", err)
				}
			} else if err != nil {
				t.Fatalf("local absence changed: %v", err)
			}
		})
	}
}
