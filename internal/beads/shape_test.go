package beads

import (
	"context"
	"strings"
	"testing"
)

func rulesOf(vs []ShapeViolation) map[string]int {
	out := map[string]int{}
	for _, v := range vs {
		out[v.Rule]++
	}
	return out
}

func ws(id string) ShapeNode {
	return ShapeNode{ID: id, Title: "Forge Notify", Status: "open", Labels: []string{"kind:objective", "kind:workstream"}, Responsible: "commander"}
}
func track(id, parent, title string) ShapeNode {
	return ShapeNode{ID: id, Parent: parent, Title: title, Status: "open", Labels: []string{"kind:objective", "kind:workstream"}, Responsible: "commander"}
}
func step(id, parent, title string, labels ...string) ShapeNode {
	return ShapeNode{ID: id, Parent: parent, Title: title, Status: "open", Labels: append([]string{"kind:objective"}, labels...), Responsible: "commander", HasReason: true}
}

// 3.1 on Nov 17: the cutover step was created twice.
func TestShapeDuplicateStep(t *testing.T) {
	nodes := []ShapeNode{ws("W"), track("W.1", "W", "Cutover Phase 1: First notifications"),
		step("W.1.7", "W.1", "Agent turns the Notify cutover on in prod (Eric's greenlight recorded) and watches 30 minutes", "w:x:un:y:prod"),
		step("W.1.9", "W.1", "Agent turns the Notify cutover on in prod (Eric's greenlight recorded) and watches 30 minutes"),
	}
	vs := ShapeCheck(nodes)
	if rulesOf(vs)["duplicate-step"] != 1 || !strings.Contains(vs[0].Fix, "supersede") {
		t.Fatalf("want one duplicate-step: %+v", vs)
	}
}

// 3.9 on Nov 17: implement listed twice, and the prod live check waiting while
// the spec greenlight and the implementation had not started.
func TestShapeImpossibleOrderAndDuplicateImplement(t *testing.T) {
	nodes := []ShapeNode{ws("W"), track("W.9", "W", "One Grafana"),
		step("W.9.1", "W.9", "Greenlight the single-Grafana spec", "gate", "approver:user"),
		step("W.9.2", "W.9", "Implement the single Grafana from the spec (implement-spec skill)"),
		step("W.9.3", "W.9", "Implement the single Grafana from the spec (implement-spec skill)"),
		step("W.9.4", "W.9", "Live check of the single Grafana on prod", "w:a:un:b:running_20prod"),
		step("W.9.5", "W.9", "Confirm done", "gate", "approver:user", "closing-gate"),
	}
	r := rulesOf(ShapeCheck(nodes))
	if r["duplicate-step"] != 1 || r["impossible-order"] < 2 {
		t.Fatalf("want duplicate and impossible order: %v", r)
	}
	// Once the earlier steps are done the same live wait is fine.
	nodes[2].Status, nodes[3].Status, nodes[4].Status = "closed", "closed", "closed"
	if r := rulesOf(ShapeCheck(nodes)); r["impossible-order"] != 0 {
		t.Fatalf("finished earlier stages still flagged: %v", r)
	}
}

// 3.4 on Nov 17: the Commander's own check placed in Eric's column, and as a
// person gate; approver:commander is the right form.
func TestShapeCommanderLane(t *testing.T) {
	nodes := []ShapeNode{ws("W"), track("W.4", "W", "Forge Console notifications"),
		step("W.4.26", "W.4", "Commander checks the green redesign PR (head, screenshots, Devin threads) before Eric's merge card", "lane:you"),
		step("W.4.27", "W.4", "Commander review: redesign PR head before Eric's merge card", "gate", "approver:user"),
		step("W.4.25", "W.4", "Commander review: redesign PR head, screenshots and Devin threads", "gate", "approver:commander"),
		step("W.4.28", "W.4", "Commander checks the screenshots", "lane:commander"),
	}
	vs := ShapeCheck(nodes)
	got := map[string]bool{}
	for _, v := range vs {
		if v.Rule == "commander-lane" {
			got[v.Beads[0]] = true
		}
	}
	if !got["W.4.26"] || !got["W.4.27"] || got["W.4.25"] || got["W.4.28"] {
		t.Fatalf("commander-lane hits %v from %+v", got, vs)
	}
}

func TestShapeEarlyConfirmDone(t *testing.T) {
	nodes := []ShapeNode{ws("W"), track("W.7", "W", "In-App Notifications"),
		step("W.7.1", "W.7", "Build in-app notifications and open its PR"),
		step("W.7.2", "W.7", "Confirm done", "gate", "approver:user", "closing-gate", "needs-user"),
	}
	if rulesOf(ShapeCheck(nodes))["early-confirm-done"] != 1 {
		t.Fatal("Confirm done asked Eric while a sibling was open")
	}
	nodes[2].Status = "closed"
	if rulesOf(ShapeCheck(nodes))["early-confirm-done"] != 0 {
		t.Fatal("flagged after the siblings finished")
	}
}

// 3.8 on Nov 17: a stale parked step with no reason; an open step with no owner.
func TestShapeOwnerAndParkedReason(t *testing.T) {
	stale := step("W.8.2", "W.8", "Forge Notify rollout and app validation", "parked")
	stale.HasReason = false
	orphan := step("W.8.3", "W.8", "Add the Slack and Teams secrets")
	orphan.Responsible = ""
	reasoned := step("W.8.4", "W.8", "Teams live check", "parked", "parked-from:open")
	nodes := []ShapeNode{ws("W"), track("W.8", "W", "Teams delivery"), stale, orphan, reasoned}
	r := rulesOf(ShapeCheck(nodes))
	if r["parked-without-reason"] != 1 || r["no-owner"] != 1 {
		t.Fatalf("want one parked-without-reason and one no-owner: %v", r)
	}
}

// Nov 16: a merge card pitched while the PR was red; the greenlight must wait
// on a PR-green or Commander check step.
func TestShapeMergeGate(t *testing.T) {
	bare := []ShapeNode{ws("W"), track("W.1", "W", "Cutover"),
		step("W.1.1", "W.1", "Build the cutover"),
		step("W.1.2", "W.1", "Greenlight: merge the cutover PR", "gate", "approver:user")}
	if rulesOf(ShapeCheck(bare))["merge-without-check"] != 1 {
		t.Fatal("merge greenlight with no check step passed")
	}
	loose := append(bare, step("W.1.3", "W.1", "Cutover PR: get CI green and answer Devin"))
	if rulesOf(ShapeCheck(loose))["merge-not-gated"] != 1 {
		t.Fatal("merge greenlight not waiting on its check passed")
	}
	loose[3].Blockers = []string{"W.1.3"}
	if r := rulesOf(ShapeCheck(loose)); r["merge-not-gated"]+r["merge-without-check"] != 0 {
		t.Fatalf("gated merge flagged: %v", r)
	}
}

// 6.1 and 6.2 on Nov 17: tracks with nothing but Confirm done.
func TestShapeEmptyTrack(t *testing.T) {
	nodes := []ShapeNode{ws("W"), track("W.13", "W", "Context control: teach app agents Temporal"),
		step("W.13.1", "W.13", "Confirm done", "gate", "approver:user", "closing-gate")}
	if rulesOf(ShapeCheck(nodes))["empty-track"] != 1 {
		t.Fatal("track with only Confirm done passed")
	}
	nodes[1].Status = "deferred"
	nodes[1].Labels = append(nodes[1].Labels, "parked", "parked-from:open")
	if rulesOf(ShapeCheck(nodes))["empty-track"] != 0 {
		t.Fatal("parked track flagged")
	}
}

func TestShapeDependencyCycle(t *testing.T) {
	a, b := step("W.1.1", "W.1", "A"), step("W.1.2", "W.1", "B")
	a.Blockers, b.Blockers = []string{"W.1.2"}, []string{"W.1.1"}
	if rulesOf(ShapeCheck([]ShapeNode{ws("W"), track("W.1", "W", "T"), a, b}))["dependency-cycle"] != 1 {
		t.Fatal("cycle passed")
	}
}

// Rule i: rebuilding a track must account for the steps already there.
func TestShapeReshapeMustAccountForOldSteps(t *testing.T) {
	before := []ShapeNode{ws("W"), track("W.1", "W", "Cutover Phase 1"),
		step("W.1.1", "W.1", "Greenlight merging each later cutover PR (one at a time)"),
		step("W.1.2", "W.1", "Live check of the moved notification")}
	ops := [][]string{
		{"create", "Turn the cutover on and watch 30 minutes", "--parent", "W.1", "--ref", "on"},
		{"create", "Check the moved notification in your inbox", "--parent", "W.1", "--ref", "inbox"},
		{"dep", "add", "$inbox", "$on"},
	}
	b := parseShapeBatch(ops)
	vs := ShapeReshape(before, b.creates, b.referenced)
	if len(vs) != 1 || !strings.Contains(vs[0].Why, "W.1.1") || !strings.Contains(vs[0].Why, "W.1.2") {
		t.Fatalf("want both old steps named: %+v", vs)
	}
	ops = append(ops, []string{"update", "W.1.1", "--status", "closed"}, []string{"dep", "add", "W.1.2", "$inbox"})
	b = parseShapeBatch(ops)
	if vs := ShapeReshape(before, b.creates, b.referenced); len(vs) != 0 {
		t.Fatalf("accounted steps still refused: %+v", vs)
	}
	// Adding one step is not a reshape.
	b = parseShapeBatch(ops[:1])
	if vs := ShapeReshape(before, b.creates, b.referenced); len(vs) != 0 {
		t.Fatalf("single addition refused: %+v", vs)
	}
}

func TestShapeNewOnlyRefusesIntroducedProblems(t *testing.T) {
	old := []ShapeViolation{{Rule: "duplicate-step", Beads: []string{"A", "B"}}}
	now := []ShapeViolation{{Rule: "duplicate-step", Beads: []string{"B", "A"}}, {Rule: "empty-track", Beads: []string{"T"}}}
	got := ShapeNew(old, now)
	if len(got) != 1 || got[0].Rule != "empty-track" {
		t.Fatalf("got %+v", got)
	}
	msg := FormatShapeRefusal(got)
	if !strings.HasPrefix(msg, "plan refused: 1 tree shape problem") || !strings.Contains(msg, "nothing was written") {
		t.Fatalf("message: %s", msg)
	}
}

// templateNodes turns template ops into the tree they would create.
func templateNodes(t *testing.T, ops [][]string, parent string) []ShapeNode {
	t.Helper()
	nodes := []ShapeNode{ws("W"), track(parent, "W", "Track")}
	ids := map[string]string{}
	idx := map[string]int{}
	for i, op := range ops {
		switch op[0] {
		case "create":
			pos, f, e := parsePGFlags(op[1:], "--parent --labels --ref --silent --description --acceptance")
			if e != nil {
				t.Fatal(e)
			}
			id := parent + "." + string(rune('a'+i))
			ids["$"+f.one("--ref")] = id
			idx[id] = len(nodes)
			under := f.one("--parent")
			if p, ok := ids[under]; ok {
				under = p
			}
			nodes = append(nodes, ShapeNode{ID: id, Parent: under, Title: pos[0], Status: "open", Labels: strings.Split(f.one("--labels"), ","), Responsible: "commander",
				HasBrief: f.one("--description") != "" && f.one("--acceptance") != ""})
		case "dep":
			from, to := ids[op[2]], ids[op[3]]
			if from == "" || to == "" {
				t.Fatalf("dep refs unresolved: %v", op)
			}
			nodes[idx[from]].Blockers = append(nodes[idx[from]].Blockers, to)
		}
	}
	return nodes
}

func TestPlanTemplatesPassShapeCheck(t *testing.T) {
	for _, name := range PlanTemplateNames() {
		for _, ownerGo := range []bool{false, true} {
			ops, e := PlanTemplateOps(name, "W.1", "redesign", ownerGo)
			if e != nil {
				t.Fatal(e)
			}
			if vs := ShapeCheck(templateNodes(t, ops, "W.1")); len(vs) != 0 {
				t.Fatalf("%s (owner go %v) fails its own check: %+v", name, ownerGo, vs)
			}
			if name == "context-teaching" {
				if vs := TreeRules(templateNodes(t, ops, "W.1")); len(vs) != 0 {
					t.Fatalf("%s breaks a numbered rule: %+v", name, vs)
				}
			}
		}
	}
	if _, e := PlanTemplateOps("nope", "W", "x", false); e == nil {
		t.Fatal("unknown template accepted")
	}
}

// The guard inside the write transaction refuses a batch that adds a
// duplicate step, and writes nothing.
func TestPostgresShapeGuardRefusesBatch(t *testing.T) {
	c, dir, _ := postgresBeads(t)
	t.Setenv("CMDR_SHAPE_CHECK", "0") // the fixture: an empty workstream alone breaks rule 2
	ctx := context.Background()
	if e := c.Init(ctx, dir); e != nil {
		t.Fatal(e)
	}
	root, e := c.Create(ctx, dir, "Workstream", CreateOptions{Labels: []string{"kind:workstream"}, Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	trk, e := c.Create(ctx, dir, "Track", CreateOptions{Parent: root, Labels: []string{"kind:workstream"}, Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("CMDR_SHAPE_CHECK", "1")
	resp := postgresApplyOps(ctx, dir, [][]string{
		{"create", "Implement the spec", "--parent", trk, "--labels", "kind:objective,lane:agents", "--description", "Implement it.", "--acceptance", "It is implemented.", "--assignee", "operator-x", "--silent"},
		{"create", "Implement the spec", "--parent", trk, "--labels", "kind:objective,lane:agents", "--description", "Implement it.", "--acceptance", "It is implemented.", "--assignee", "operator-x", "--silent"},
	})
	if resp.Exit == 0 || !strings.Contains(resp.Error, "duplicate-step") || !strings.Contains(resp.Error, "nothing was written") {
		t.Fatalf("duplicate batch not refused: %+v", resp)
	}
	listed, e := c.List(ctx, dir, ListOptions{Status: "all"})
	if e != nil || len(listed) != 2 {
		t.Fatalf("refused batch wrote rows: %d %v", len(listed), e)
	}
	resp = postgresApplyOps(ctx, dir, [][]string{{"create", "Implement the spec", "--parent", trk, "--labels", "kind:objective,lane:agents", "--description", "Implement it.", "--acceptance", "It is implemented.", "--assignee", "operator-x", "--silent"}})
	if resp.Exit != 0 {
		t.Fatalf("valid single step refused: %+v", resp)
	}
}

// The teaching template stamps out the workstream and the shape of 596t.15:
// implementer steps, independent reviews, no PR, and Eric's one final review.
func TestContextTeachingTemplate(t *testing.T) {
	ops, e := PlanTemplateOps("context-teaching", "W.1", "Temporal schedules", false)
	if e != nil {
		t.Fatal(e)
	}
	nodes := templateNodes(t, ops, "W.1")
	var ws *ShapeNode
	byOwner := map[string]int{}
	for i := range nodes[2:] {
		n := &nodes[2+i]
		switch {
		case n.has("kind:workstream"):
			ws = n
		case n.gate():
			byOwner["you"]++
		case n.has("review"):
			byOwner["reviewer"]++
		default:
			byOwner["implementer"]++
		}
		if strings.Contains(strings.ToLower(n.Title), " pr ") && !strings.Contains(n.Title, "no PR") {
			t.Errorf("a teaching step makes a PR: %q", n.Title)
		}
	}
	if ws == nil || ws.Title != "Temporal schedules" || ws.Parent != "W.1" {
		t.Fatalf("workstream: %+v", ws)
	}
	if byOwner["you"] != 1 || byOwner["reviewer"] != 2 || byOwner["implementer"] != 5 {
		t.Fatalf("owners: %v", byOwner)
	}
	for _, n := range nodes[2:] {
		if n.ID != ws.ID && n.Parent != ws.ID {
			t.Errorf("%q is not under the new workstream", n.Title)
		}
	}
}
