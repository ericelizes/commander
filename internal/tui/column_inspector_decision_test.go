package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/example-user/commander/internal/inspector"
	"github.com/example-user/commander/internal/store"
)

const cardJSON = `{"question":"Merge the import stack?","context":"CI is green on all four PRs.","options":[{"label":"Merge now","implications":"ships to staging today","recommended":true},{"label":"Wait for reviewer-a","implications":"slips a day"}],"input":true,"steps":["gh pr merge 4107"]}`

func decisionColumn(t *testing.T) (*InspectorColumn, *store.Store) {
	t.Helper()
	c, st := newInspectorForTest(t, 70, 40)
	ctx := context.Background()
	if err := st.UpsertNeedsYou(ctx, store.NeedsYou{ProjectID: 0, BeadID: "ws.1", Ask: "Merge?", Decision: cardJSON}); err != nil {
		t.Fatal(err)
	}
	id, _, err := st.AddTab(ctx, store.InspectorTab{Title: "⚑ Merge?", Kind: inspector.KindDecision, Target: "ws.1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveTab(ctx, 0, "", id); err != nil {
		t.Fatal(err)
	}
	drive(t, c, tea.WindowSizeMsg{Width: 70, Height: 40})
	c.focused = true
	drive(t, c, tea.FocusMsg{})
	return c, st
}

func TestDecisionCard_RendersQuestionOptionsRecommendedAndSteps(t *testing.T) {
	c, _ := decisionColumn(t)
	view := ansi.Strip(c.innerView())
	for _, want := range []string{"⚑ DECISION", "Merge the import stack?", "CI is green", "○ 1  Merge now", "★ recommended", "ships to staging today", "○ 2  Wait for reviewer-a", "gh pr merge 4107", "add a note", "Submit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("card missing %q:\n%s", want, view)
		}
	}
}

func TestDecisionCard_NumberKeyPicksAndSubmitNeedsAChoice(t *testing.T) {
	c, _ := decisionColumn(t)
	drive(t, c, key("enter"))
	if !strings.Contains(c.status, "pick an option") {
		t.Fatalf("submitting with nothing chosen should say so: %q", c.status)
	}
	drive(t, c, key("2"))
	if view := ansi.Strip(c.innerView()); !strings.Contains(view, "● 2  Wait for reviewer-a") {
		t.Fatalf("2 selects option 2:\n%s", view)
	}
	// 1-9 pick options on a card (they switch tabs elsewhere).
	if tab := c.activeTab(); tab == nil || tab.Kind != inspector.KindDecision {
		t.Fatal("the card stays the active tab")
	}
}

func TestDecisionCard_MouseClickSelectsAnOption(t *testing.T) {
	c, _ := decisionColumn(t)
	c.innerView() // lay out the hit rows
	var row int
	for _, h := range c.decHits {
		if h.kind == "opt" && h.idx == 0 {
			row = h.from
		}
	}
	drive(t, c, tea.MouseMsg{X: panelPadX + 3, Y: row + panelPadY, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if view := ansi.Strip(c.innerView()); !strings.Contains(view, "● 1  Merge now") {
		t.Fatalf("click selects option 1:\n%s", view)
	}
}

func TestDecisionCard_SubmitSendsAndConfirms_AnsweredFlagShowsNoLongerNeeded(t *testing.T) {
	c, st := decisionColumn(t)
	drive(t, c, key("1"))
	drive(t, c, key("i"))
	typeText(t, c, "after lunch")
	drive(t, c, key("enter")) // keeps the note
	if _, s := c.activeDecision(); s.note != "after lunch" {
		t.Fatalf("note = %q", s.note)
	}
	if choice, ok := func() (string, bool) { _, s := c.activeDecision(); return s.ready() }(); !ok || choice != "Merge now" {
		t.Fatalf("ready = %q %v", choice, ok)
	}
	// The submit command shells out; feed its result as the column would receive it.
	tab, s := c.activeDecision()
	_ = c.submitDecision(tab, s)
	if !s.sending {
		t.Fatal("submitting marks the card as sending")
	}
	drive(t, c, decisionSentMsg{tabID: tab.ID, choice: "Merge now"})
	if view := ansi.Strip(c.innerView()); !strings.Contains(view, "✓ ANSWERED") || !strings.Contains(view, "Sent to the Manager: Merge now") {
		t.Fatalf("confirmation missing:\n%s", view)
	}

	// A flag that was cleared elsewhere reads "no longer needed".
	if err := st.DeleteNeedsYou(context.Background(), 0, "ws.1"); err != nil {
		t.Fatal(err)
	}
	c2, _ := decisionColumn(t)
	_, s2 := c2.activeDecision()
	s2.rec = decisionRecord{}
	if view := ansi.Strip(c2.innerView()); !strings.Contains(view, "no longer needed") {
		t.Fatalf("withdrawn card:\n%s", view)
	}
}
