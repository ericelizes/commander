package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

type retirementClient struct {
	beads.Client
	failLabel string
	failAudit bool
	lostAudit string
	lost      bool
	mutate    func()
}

func (c *retirementClient) SetLabel(ctx context.Context, dir, id, label string, on bool) error {
	if c.failLabel == label {
		c.failLabel = ""
		if c.lost {
			_ = c.Client.SetLabel(ctx, dir, id, label, on)
		}
		return errors.New("synthetic interrupted label write")
	}
	return c.Client.SetLabel(ctx, dir, id, label, on)
}
func (c *retirementClient) AddComment(ctx context.Context, dir, id, text string) error {
	if c.failAudit || (strings.HasSuffix(text, " completed") && c.failLabel == "completion") {
		return errors.New("synthetic audit failure")
	}
	err := c.Client.AddComment(ctx, dir, id, text)
	if c.lostAudit == "intent" || (c.lostAudit == "completion" && strings.HasSuffix(text, " completed")) {
		c.lostAudit = ""
		return errors.New("synthetic lost audit response")
	}
	if c.mutate != nil {
		fn := c.mutate
		c.mutate = nil
		fn()
	}
	return err
}

func retirementFixture(t *testing.T) (*beads.MemClient, *store.Store, []string) {
	t.Helper()
	m := setupGateProject(t)
	m.EdgeParentsOnly = true
	ctx := context.Background()
	for _, b := range []beads.Bead{
		{ID: "sample-flow", Labels: []string{"kind:objective"}},
		{ID: "sample-flow.2", Title: "Execution stage", Description: "Preserve this body", Labels: []string{"kind:objective", "gate", "approver:user", "owner:manager-sample-flow"}},
		{ID: "sample-flow.3", Title: "Approve execution", Description: "Original approval context", Labels: []string{"kind:gate", "gate-of:sample-flow_2e2", "gate-name:approval", "verifier:user", "approver:user", "needs-user"}},
	} {
		m.Seed(b)
	}
	_ = m.SetParent(ctx, "/d", "sample-flow.2", "sample-flow")
	_ = m.SetParent(ctx, "/d", "sample-flow.3", "sample-flow")
	_ = m.DependOn(ctx, "/d", "sample-flow.2", "sample-flow.3")
	st, _, err := needsYouStoreFn()
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.GetProjectByName(ctx, "manifest")
	if err != nil {
		t.Fatal(err)
	}
	if err = st.UpsertNeedsYou(ctx, store.NeedsYou{ProjectID: p.ID, BeadID: "sample-flow.3", Ask: "Approve execution", Decision: "{ \"question\": \"Proceed?\", \"options\": [{\"label\":\"Approve\"},{\"label\":\"Wait\"}] }", Note: "Retain bytes", Since: 123}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMMANDER_ROLE", "manager-sample-flow")
	return m, st, []string{"retire-legacy", "--project", "manifest", "--bead", "sample-flow.2", "--replacement", "sample-flow.3", "--reason", "Retire redundant legacy markers while retaining the approval"}
}
func retirementSnapshot(t *testing.T, m beads.Client, st *store.Store) string {
	t.Helper()
	ctx := context.Background()
	g, e := m.Show(ctx, "/d", "sample-flow.3")
	if e != nil {
		t.Fatal(e)
	}
	d, e := m.Details(ctx, "/d", []string{"sample-flow.3"})
	if e != nil {
		t.Fatal(e)
	}
	p, _ := st.GetProjectByName(ctx, "manifest")
	n, e := st.GetNeedsYou(ctx, p.ID, "sample-flow.3")
	if e != nil {
		t.Fatal(e)
	}
	ps, e := st.ListPitches(ctx, p.ID, "sample-flow.3")
	if e != nil {
		t.Fatal(e)
	}
	rs, e := st.ListNeedsResolutions(ctx, p.ID, "sample-flow.3")
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal([]any{g, d, n, ps, rs})
	return string(raw)
}
func TestGateRetireLegacy(t *testing.T) {
	m, st, args := retirementFixture(t)
	before := retirementSnapshot(t, m, st)
	original, _ := m.Show(context.Background(), "/d", "sample-flow.2")
	raw, _ := json.Marshal(original)
	var want beads.Bead
	_ = json.Unmarshal(raw, &want)
	want.Labels = slices.DeleteFunc(want.Labels, func(l string) bool { return l == "gate" || l == "approver:user" })
	if code, _, msg := gateCmd(t, append(args, "--dry-run")...); code != 0 {
		t.Fatal(msg)
	}
	ds, _ := m.Details(context.Background(), "/d", []string{"sample-flow.2"})
	if len(ds["sample-flow.2"].Comments) != 0 {
		t.Fatal("dry-run wrote audit")
	}
	for i := 0; i < 2; i++ {
		if code, _, msg := gateCmd(t, args...); code != 0 {
			t.Fatal(msg)
		}
	}
	after := retirementSnapshot(t, m, st)
	if before != after {
		t.Fatal("replacement bead/card/generation changed")
	}
	got, _ := m.Show(context.Background(), "/d", "sample-flow.2")
	if !reflect.DeepEqual(want, *got) {
		t.Fatalf("stage changed beyond markers: %+v", got)
	}
	ds, _ = m.Details(context.Background(), "/d", []string{"sample-flow.2"})
	if len(ds["sample-flow.2"].Comments) != 2 {
		t.Fatal("audit not idempotent", ds)
	}
}
func TestGateRetireLegacyRefusals(t *testing.T) {
	for _, tc := range []string{"foreign", "operator", "actual-gate", "closed-stage", "closed-gate", "held", "proposed", "answered", "stage-card", "multiple", "wrong-link", "foreign-parent", "missing-card", "unaudited", "read-failure", "held-ancestor", "no-wait", "answered-marker", "ambiguous-parent"} {
		t.Run(tc, func(t *testing.T) {
			m, st, args := retirementFixture(t)
			ctx := context.Background()
			p, _ := st.GetProjectByName(ctx, "manifest")
			switch tc {
			case "foreign":
				t.Setenv("COMMANDER_ROLE", "manager-foreign")
			case "operator":
				t.Setenv("COMMANDER_ROLE", "operator-sample")
			case "actual-gate":
				_ = m.SetLabel(ctx, "/d", "sample-flow.2", "kind:gate", true)
			case "closed-stage":
				_ = m.SetStatus(ctx, "/d", "sample-flow.2", "closed")
			case "closed-gate":
				_ = m.SetStatus(ctx, "/d", "sample-flow.3", "closed")
			case "held":
				_ = m.SetLabel(ctx, "/d", "sample-flow.3", "hold", true)
			case "proposed":
				_ = m.SetLabel(ctx, "/d", "sample-flow.3", "proposed:ask:123", true)
			case "answered":
				_ = st.AnswerPitch(ctx, p.ID, "sample-flow.3", "Approve")
			case "stage-card":
				_ = st.UpsertNeedsYou(ctx, store.NeedsYou{ProjectID: p.ID, BeadID: "sample-flow.2", Ask: "Separate decision", Decision: "separate"})
			case "multiple":
				m.Seed(beads.Bead{ID: "sample-extra", Labels: []string{"kind:gate", "gate-of:sample-flow_2e2"}})
			case "wrong-link":
				_ = m.SetLabel(ctx, "/d", "sample-flow.3", "gate-of:other", true)
			case "foreign-parent":
				_ = m.SetParent(ctx, "/d", "sample-flow.3", "elsewhere")
			case "missing-card":
				_ = st.DeleteNeedsYou(ctx, p.ID, "sample-flow.3")
			case "unaudited":
				_ = m.SetLabel(ctx, "/d", "sample-flow.2", "gate", false)
			case "held-ancestor":
				_ = m.SetLabel(ctx, "/d", "sample-flow", "hold", true)
			case "no-wait":
				_ = m.RemoveDep(ctx, "/d", "sample-flow.2", "sample-flow.3")
			case "answered-marker":
				_ = m.SetLabel(ctx, "/d", "sample-flow.3", beads.DecisionAnsweredLabel, true)
			case "ambiguous-parent":
				m.Edges = append(m.Edges, beads.Dep{IssueID: "sample-flow.3", DependsOnID: "other", Type: "parent-child"})
			case "read-failure":
				newBeadsClientFn = func() beads.Client { return &beads.MockClient{} }
			}
			before, _ := json.Marshal(m.Beads)
			if code, _, msg := gateCmd(t, args...); code == 0 {
				t.Fatal("accepted", tc, msg)
			}
			after, _ := json.Marshal(m.Beads)
			if string(before) != string(after) {
				t.Fatal("refusal wrote")
			}
			ds, _ := m.Details(ctx, "/d", []string{"sample-flow.2"})
			if len(ds["sample-flow.2"].Comments) != 0 {
				t.Fatal("refusal audited")
			}
		})
	}
}
func TestGateRetireLegacyRecovery(t *testing.T) {
	for _, tc := range []string{"audit", "first-label", "second-label", "lost-first", "lost-second", "changed-card", "completion", "lost-intent", "lost-completion"} {
		t.Run(tc, func(t *testing.T) {
			m, st, args := retirementFixture(t)
			ctx := context.Background()
			c := &retirementClient{Client: m}
			switch tc {
			case "lost-intent":
				c.lostAudit = "intent"
			case "lost-completion":
				c.lostAudit = "completion"
			case "completion":
				c.failLabel = "completion"
			case "audit":
				c.failAudit = true
			case "first-label":
				c.failLabel = "gate"
			case "second-label":
				c.failLabel = "approver:user"
			case "lost-first":
				c.failLabel = "gate"
				c.lost = true
			case "lost-second":
				c.failLabel = "approver:user"
				c.lost = true
			case "changed-card":
				c.mutate = func() { _ = m.SetLabel(ctx, "/d", "sample-flow.3", "hold", true) }
			}
			newBeadsClientFn = func() beads.Client { return c }
			before := retirementSnapshot(t, m, st)
			if code, _, msg := gateCmd(t, args...); code == 0 {
				t.Fatal("expected interruption", msg)
			}
			if tc == "changed-card" {
				b, _ := m.Show(ctx, "/d", "sample-flow.2")
				if !slices.Contains(b.Labels, "gate") {
					t.Fatal("wrote after evidence changed")
				}
				return
			}
			c.failAudit = false
			c.failLabel = ""
			if code, _, msg := gateCmd(t, args...); code != 0 {
				t.Fatal(msg)
			}
			if before != retirementSnapshot(t, m, st) {
				t.Fatal("replacement changed")
			}
			changed := slices.Clone(args)
			changed[len(changed)-1] = "Different request"
			if code, _, _ := gateCmd(t, changed...); code == 0 {
				t.Fatal("unaudited retry accepted")
			}
		})
	}
}
func TestRetireLegacyScenario(t *testing.T) {
	data, err := os.ReadFile("../../tests/evals/scenarios/retire-legacy.json")
	if err != nil {
		t.Fatal(err)
	}
	var s struct{ Root, Stage, Replacement, Reason string }
	if err = json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	m, st, args := retirementFixture(t)
	if s.Root != "sample-flow" || s.Stage != args[4] || s.Replacement != args[6] || s.Reason != args[8] {
		t.Fatal("scenario/fixture mismatch")
	}
	before := retirementSnapshot(t, m, st)
	if code, out, msg := gateCmd(t, args...); code != 0 {
		t.Fatal(msg)
	} else {
		t.Log(strings.TrimSpace(out))
	}
	if before != retirementSnapshot(t, m, st) {
		t.Fatal("scenario lost genuine card")
	}
	cards, err := st.ListNeedsYou(context.Background())
	if err != nil || len(cards) != 1 || cards[0].BeadID != s.Replacement {
		t.Fatal("sole live card not preserved", cards, err)
	}
	t.Log("legacy markers removed; sole live replacement card, generation, raw JSON, linkage and unapproved state preserved")
}
func TestRetireLegacyAuditReserved(t *testing.T) {
	retirementFixture(t)
	out, errOut, read := withTempOutputFiles(t)
	code := runInternalComment([]string{"--project", "manifest", "--bead", "sample-flow.2", "--text", "\n " + legacyRetirementPrefix + "{}"}, strings.NewReader(""), out, errOut)
	_, msg := read()
	if code == 0 || !strings.Contains(msg, "only by internal-gate retire-legacy") {
		t.Fatal(code, msg)
	}
}

func TestGateRetireLegacyAuthorizedRoles(t *testing.T) {
	for _, role := range []string{"", "commander", "manager-sample-flow"} {
		t.Run("role="+role, func(t *testing.T) {
			_, _, args := retirementFixture(t)
			t.Setenv("COMMANDER_ROLE", role)
			t.Setenv("COMMANDER_MANAGER_ROLE", "")
			if code, _, msg := gateCmd(t, args...); code != 0 {
				t.Fatal(msg)
			}
		})
	}
}
func TestGateRetireLegacyInheritedOperatorRefuses(t *testing.T) {
	_, _, args := retirementFixture(t)
	t.Setenv("COMMANDER_ROLE", "")
	t.Setenv("COMMANDER_MANAGER_ROLE", "manager-sample-flow")
	if code, _, _ := gateCmd(t, args...); code == 0 {
		t.Fatal("legacy host Operator authorized as user")
	}
}

func TestGateRetireLegacyChangedEvidenceRefusesPartialRetry(t *testing.T) {
	m, _, args := retirementFixture(t)
	c := &retirementClient{Client: m, failLabel: "approver:user"}
	newBeadsClientFn = func() beads.Client { return c }
	if code, _, _ := gateCmd(t, args...); code == 0 {
		t.Fatal("expected partial interruption")
	}
	// An unchanged state would permit recovery, but newly retained replacement
	// prose must not silently reuse the original audit receipt.
	_ = m.AddComment(context.Background(), "/d", "sample-flow.3", "Synthetic new approval context")
	if code, _, _ := gateCmd(t, args...); code == 0 {
		t.Fatal("changed evidence accepted")
	}
	stage, _ := m.Show(context.Background(), "/d", "sample-flow.2")
	if !slices.Contains(stage.Labels, "approver:user") {
		t.Fatal("partial retry wrote after drift")
	}
}

func TestRetireLegacyParkedStageRetainsReplacement(t *testing.T) {
	m, st, args := retirementFixture(t)
	if err := m.SetStatus(context.Background(), "", args[4], beads.StatusDeferred); err != nil {
		t.Fatal(err)
	}
	before := retirementSnapshot(t, m, st)
	if code, _, msg := gateCmd(t, args...); code != 0 {
		t.Fatal(msg)
	}
	if after := retirementSnapshot(t, m, st); after != before {
		t.Fatal("retirement changed retained user approval")
	}
	b, err := m.Show(context.Background(), "", args[4])
	if err != nil || b.Status != beads.StatusDeferred {
		t.Fatal("parked stage resumed", b, err)
	}
}
