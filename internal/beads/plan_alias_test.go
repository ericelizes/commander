package beads

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestPostgresPlanValidatesResolvedBeadsInsideTransaction(t *testing.T) {
	c, dir, st := postgresBeads(t)
	ctx := context.Background()
	if e := c.Init(ctx, dir); e != nil {
		t.Fatal(e)
	}
	basis := PlanBasis{JurisdictionRevision: 1, Beads: map[string]int64{}}
	sawCreated := false
	req := WithPlanValidator(WithPlanBasis(ctx, &basis), func(checkCtx context.Context, ops [][]string) error {
		if ops[0][0] != "update" {
			return nil
		}
		b, e := c.Show(checkCtx, dir, ops[0][1])
		if e != nil {
			return e
		}
		if b.Title != "New guarded task" {
			t.Fatalf("guard saw wrong bead: %+v", b)
		}
		sawCreated = true
		return errors.New("approval boundary refuses the update")
	})
	r := postgresApplyOps(req, dir, [][]string{{"create", "New guarded task", "--ref", "task"}, {"update", "$task", "--title", "Unapproved"}})
	if r.Exit == 0 || !sawCreated {
		t.Fatalf("guard did not read uncommitted resolved task: %+v, seen=%t", r, sawCreated)
	}
	var n int
	if e := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM bead_tasks`).Scan(&n); e != nil || n != 0 {
		t.Fatal("guard refusal leaked first operation", n, e)
	}
	if e := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM commander_plan_receipts`).Scan(&n); e != nil || n != 0 {
		t.Fatal("guard refusal retained success receipt", n, e)
	}
}

func TestPostgresPlanRejectsForwardAndDuplicateAliasesBeforeWrites(t *testing.T) {
	for _, ops := range [][][]string{
		{{"create", "Child", "--parent", "$later"}, {"create", "Parent", "--ref", "later"}},
		{{"create", "One", "--ref", "same"}, {"create", "Two", "--ref", "same"}},
		{{"create", "Invalid", "--ref", "not valid"}},
	} {
		t.Run(ops[0][1], func(t *testing.T) {
			c, dir, st := postgresBeads(t)
			ctx := context.Background()
			if e := c.Init(ctx, dir); e != nil {
				t.Fatal(e)
			}
			basis := PlanBasis{JurisdictionRevision: 1, Beads: map[string]int64{}}
			r := postgresApplyOps(WithPlanBasis(ctx, &basis), dir, ops)
			if r.Exit == 0 {
				t.Fatal("invalid aliases committed")
			}
			var n int
			if e := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM bead_tasks`).Scan(&n); e != nil || n != 0 {
				t.Fatal("invalid alias leaked beads", n, e)
			}
		})
	}
}

func TestPlanAliasResolutionPreservesFlagValuesAndOptionOrder(t *testing.T) {
	aliases := map[string]string{"task": "real-task", "goal": "real-goal"}
	tests := []struct {
		op, want []string
		ref      string
	}{
		{[]string{"update", "--description", "$goal", "$task", "--parent=$goal"}, []string{"update", "--description", "$goal", "real-task", "--parent=real-goal"}, ""},
		{[]string{"create", "Literal", "--description", "--ref", "--ref", "task", "--parent", "$goal"}, []string{"create", "Literal", "--description", "--ref", "--parent", "real-goal", "--silent"}, "task"},
		{[]string{"comments", "add", "$task", "--ref $goal"}, []string{"comments", "add", "real-task", "--ref $goal"}, ""},
	}
	for _, test := range tests {
		actual, ref, e := resolvePlanOperation(test.op, aliases)
		if e != nil || ref != test.ref || !reflect.DeepEqual(actual, test.want) {
			t.Fatalf("resolution=%v ref=%s error=%v, want=%v", actual, ref, e, test.want)
		}
	}
}
