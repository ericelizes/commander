package beads

import (
	"context"
	"strings"
	"testing"
)

func TestPostgresPlanningRetiresParkedLegacyAutoGate(t *testing.T) {
	c, dir, _, ctx, _, _, _, _ := humanAuthorityFixture(t)
	user := WithDoorEnv(context.Background(), nil)
	parent, e := c.Create(user, dir, "Commander", CreateOptions{Labels: []string{"kind:objective", WorkstreamLabel}})
	if e != nil {
		t.Fatal(e)
	}
	stage, e := c.Create(user, dir, "Reviewed board repair fast-forwarded to local main", CreateOptions{Parent: parent, Labels: []string{"kind:objective"}})
	if e != nil {
		t.Fatal(e)
	}
	if e = c.SetStatus(user, dir, stage, StatusDeferred); e != nil {
		t.Fatal(e)
	}
	gate, e := c.Create(user, dir, "gate merged", CreateOptions{Parent: parent, Labels: []string{"kind:gate", "verifier:auto", "gate-name:merged", "gate-of:" + EscapeLabel(stage), "gate-status:failed"}})
	if e != nil {
		t.Fatal(e)
	}
	if e = c.CloseWithReason(ctx, dir, stage, "superseded"); e == nil {
		t.Fatal("ordinary close unexpectedly passed failed gate")
	}
	if _, _, e = postgresRun(ctx, dir, []string{"retire", stage, "--mode", "legacy-stage", "--reason", "superseded local delivery"}); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{stage, gate} {
		b, e := c.Show(user, dir, id)
		if e != nil || b.Status != "closed" || !Superseded(*b) {
			t.Fatalf("%s: %+v %v", id, b, e)
		}
	}
	b, _ := c.Show(user, dir, gate)
	if !strings.Contains(strings.Join(b.Labels, ","), "gate-status:failed") {
		t.Fatal("failed outcome lost")
	}
}

func TestPostgresPlanningRetiresWholeWorkflowWithoutWriters(t *testing.T) {
	c, dir, _, ctx, _, _, _, _ := humanAuthorityFixture(t)
	user := WithDoorEnv(context.Background(), nil)
	labels := []string{"kind:objective", "workflow-template:pr-review", "workflow-instance:old-review", "workflow-hash:saved"}
	block, e := c.Create(user, dir, "Review gate", CreateOptions{Labels: labels})
	if e != nil {
		t.Fatal(e)
	}
	var steps []string
	for _, step := range []string{"writers", "review", "report"} {
		id, e := c.Create(user, dir, step, CreateOptions{Parent: block, Labels: append(append([]string{}, labels...), "workflow-step:"+step)})
		if e != nil {
			t.Fatal(e)
		}
		steps = append(steps, id)
	}
	for i := 1; i < len(steps); i++ {
		if e = c.DependOn(user, dir, steps[i], steps[i-1]); e != nil {
			t.Fatal(e)
		}
	}
	if _, _, e = postgresRun(ctx, dir, []string{"retire", block, "--mode", "workflow-block", "--reason", "withdrawn obsolete PR review"}); e != nil {
		t.Fatal(e)
	}
	for _, id := range append(steps, block) {
		b, e := c.Show(user, dir, id)
		if e != nil || b.Status != "closed" || !Superseded(*b) {
			t.Fatalf("%s: %+v %v", id, b, e)
		}
	}
	d, e := c.Details(user, dir, steps)
	if e != nil {
		t.Fatal(e)
	}
	if len(d[steps[2]].Deps) < 2 {
		t.Fatal("predecessor history lost")
	}
}

func TestPostgresRetirementCannotPassUserGateOrCloseWorkstream(t *testing.T) {
	c, dir, st, ctx, pid, _, gate, _ := humanAuthorityFixture(t)
	user := WithDoorEnv(context.Background(), nil)
	for _, labels := range [][]string{{"kind:objective", WorkstreamLabel}, {"kind:objective", ClosingGateLabel, "kind:gate", "verifier:user"}, {"kind:objective", "hold"}} {
		id, e := c.Create(user, dir, "protected", CreateOptions{Labels: labels})
		if e != nil {
			t.Fatal(e)
		}
		for _, mode := range []string{"legacy-stage", "workflow-block"} {
			if _, _, e = postgresRun(ctx, dir, []string{"retire", id, "--mode", mode, "--reason", "superseded"}); e == nil {
				t.Fatalf("retired protected %v", labels)
			}
		}
	}
	if _, _, e := postgresRun(ctx, dir, []string{"retire", gate, "--mode", "legacy-stage", "--reason", "superseded"}); e == nil {
		t.Fatal("user gate retired")
	}
	legacy, e := c.Create(user, dir, "Registered legacy workstream", CreateOptions{Labels: []string{"kind:objective"}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(user, `INSERT INTO workstream_managers(project_id,workstream_bead_id,am_role,session_file,created_at) VALUES(?,?,?,'private',1)`, pid, legacy, "manager-"+legacy); e != nil {
		t.Fatal(e)
	}
	if _, _, e = postgresRun(ctx, dir, []string{"retire", legacy, "--mode", "legacy-stage", "--reason", "superseded"}); e == nil {
		t.Fatal("registered workstream retired")
	}
}

func TestPostgresWorkflowRetirementRefusesWholeScopeWithUserStep(t *testing.T) {
	c, dir, st, ctx, pid, _, _, _ := humanAuthorityFixture(t)
	user := WithDoorEnv(context.Background(), nil)
	labels := []string{"kind:objective", "workflow-template:pr-review", "workflow-instance:old-review", "workflow-hash:saved"}
	block, e := c.Create(user, dir, "Obsolete block", CreateOptions{Labels: labels})
	if e != nil {
		t.Fatal(e)
	}
	first, e := c.Create(user, dir, "First review", CreateOptions{Parent: block, Labels: append(append([]string{}, labels...), "workflow-step:first")})
	if e != nil {
		t.Fatal(e)
	}
	protected := append(append([]string{}, labels...), "workflow-step:last", "verifier:user", "kind:gate")
	if _, e = c.Create(user, dir, "User approval", CreateOptions{Parent: block, Labels: protected}); e != nil {
		t.Fatal(e)
	}
	if _, _, e = postgresRun(ctx, dir, []string{"retire", block, "--mode", "workflow-block", "--reason", "withdrawn"}); e == nil {
		t.Fatal("retired block containing user gate")
	}
	for _, id := range []string{block, first} {
		b, e := c.Show(user, dir, id)
		if e != nil || b.Status != "open" || Superseded(*b) {
			t.Fatalf("refusal partially retired %s: %+v %v", id, b, e)
		}
	}
	var comments int
	if e = st.DB().QueryRowContext(user, `SELECT COUNT(*) FROM bead_comments WHERE project_id=? AND bead_id=?`, pid, first).Scan(&comments); e != nil || comments != 0 {
		t.Fatal("refusal wrote audit", comments, e)
	}
}

func TestPostgresWorkflowRetirementPreservesSeparateUserGate(t *testing.T) {
	c, dir, _, ctx, _, _, _, _ := humanAuthorityFixture(t)
	user := WithDoorEnv(context.Background(), nil)
	labels := []string{"kind:objective", "workflow-template:pr-review", "workflow-instance:old-review", "workflow-hash:saved"}
	block, e := c.Create(user, dir, "Obsolete block", CreateOptions{Labels: labels})
	if e != nil {
		t.Fatal(e)
	}
	step, e := c.Create(user, dir, "First review", CreateOptions{Parent: block, Labels: append(append([]string{}, labels...), "workflow-step:first")})
	if e != nil {
		t.Fatal(e)
	}
	gate, e := c.Create(user, dir, "Separate approval", CreateOptions{Labels: []string{"kind:gate", "verifier:user", "gate-of:" + EscapeLabel(step)}})
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = postgresRun(ctx, dir, []string{"retire", block, "--mode", "workflow-block", "--reason", "withdrawn"}); e == nil || !strings.Contains(e.Error(), "attached gate") {
		t.Fatalf("attached user gate hidden: %v", e)
	}
	for _, id := range []string{block, step, gate} {
		b, e := c.Show(user, dir, id)
		if e != nil || b.Status != "open" || Superseded(*b) {
			t.Fatalf("protected scope changed: %s %+v %v", id, b, e)
		}
	}
}
