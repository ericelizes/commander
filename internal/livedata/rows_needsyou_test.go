package livedata

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

func flagged(id string) SnapStep {
	s := leaf(id, "", "", "")
	s.State = "needs-user"
	return s
}

// An earlier active stage must not hide a later gate that waits on the user.
func TestRowsNeedsYou_BeatsActiveWork(t *testing.T) {
	ask := gateStep("w.1.2", "Repair approach approved", []string{"user"})
	ask.State = "needs-user"
	ws := SnapWorkstream{BeadID: "w", Steps: []SnapStep{
		leaf("w.1.1", "operator-a", "working", ""),
		ask,
		gateStep("w.12.2", "Workflow approved", []string{"user"}),
		gateStep("w.9", "PR approval", []string{"user"}, "w.1.1"),
	}}
	r := Rows(snapOf(ws, idle, NeedsYou{BeadID: "w.1.2"}), nowSec)[0]
	if r.Line.Kind != "needs" || r.Colour != "red" {
		t.Fatalf("line %+v colour %q", r.Line, r.Colour)
	}
	if r.NextGateID != "w.1.2" {
		t.Fatalf("next gate %q", r.NextGateID)
	}
}

// Open user cards in a track outrank the root's waits and operators.
func TestRowsNeedsYou_TrackCardsOutrankWaits(t *testing.T) {
	track := SnapStep{BeadID: "w.5", Title: "Track", Status: "open", State: "in-progress", Children: []SnapStep{flagged("w.5.1")}}
	ws := SnapWorkstream{BeadID: "w", Steps: []SnapStep{
		track,
		leaf("w.2", "operator-a", "working", "CI #1"),
		gateStep("w.9", "Merge approval", []string{"reviewer"}, "w.2"),
	}}
	r := Rows(snapOf(ws, idle, NeedsYou{BeadID: "w.5.1"}), nowSec)[0]
	if r.Line.Kind != "needs" {
		t.Fatalf("line %+v", r.Line)
	}
}

func TestRowsNeedsYou_DeferredStaysParked(t *testing.T) {
	parked := flagged("w.5.1")
	parked.Status = "deferred"
	ws := SnapWorkstream{BeadID: "w", Steps: []SnapStep{
		leaf("w.1", "operator-a", "working", ""),
		parked,
		gateStep("w.9", "PR approval", []string{"user"}, "w.1"),
	}}
	r := Rows(snapOf(ws, idle), nowSec)[0]
	if r.Line.Kind != "working" {
		t.Fatalf("line %+v", r.Line)
	}
}

type needsYouFixture struct {
	Now   int64     `json:"now"`
	Agent SnapAgent `json:"agent"`
	Cases []struct {
		Name   string     `json:"name"`
		Steps  []SnapStep `json:"steps"`
		Needs  []string   `json:"needs"`
		Expect struct {
			Kind  string `json:"kind"`
			Text  string `json:"text"`
			Since int64  `json:"since"`
		} `json:"expect"`
	} `json:"cases"`
}

var pct = regexp.MustCompile(` · \d+%$`)

// The same fixtures app/test/needsyou-row.test.ts reads: Go and TS must agree.
func TestRowsNeedsYou_SharedFixtures(t *testing.T) {
	SetTicketPrefix("TEAM") // the fixtures' ticket prefix
	t.Cleanup(func() { SetTicketPrefix("") })
	raw, err := os.ReadFile("../../app/test/fixtures/needsyou-rows.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx needsYouFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	for _, c := range fx.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var needs []NeedsYou
			for _, id := range c.Needs {
				needs = append(needs, NeedsYou{BeadID: id})
			}
			r := Rows(snapOf(SnapWorkstream{BeadID: "w", Steps: c.Steps}, fx.Agent, needs...), fx.Now)[0]
			c.Expect.Text = pct.ReplaceAllString(c.Expect.Text, "") // the board's text leaves progress to its own field
			if r.Line.Kind != c.Expect.Kind || r.Line.Text != c.Expect.Text || r.Line.Since != c.Expect.Since {
				t.Fatalf("got %q %q since %d; want %+v", r.Line.Kind, r.Line.Text, r.Line.Since, c.Expect)
			}
		})
	}
}

// A workstream with no gates names its first card too.
func TestRowsNeedsYou_NoGatesNamesFirstCard(t *testing.T) {
	a, b := flagged("w.1"), flagged("w.2")
	a.Title, b.Title = "Post the replies", "Merge card"
	ws := SnapWorkstream{BeadID: "w", Steps: []SnapStep{a, b}}
	r := Rows(snapOf(ws, idle, NeedsYou{BeadID: "w.2"}, NeedsYou{BeadID: "w.1"}), nowSec)[0]
	if r.Line.Text != "Needs you: Merge card (+1 more)" {
		t.Fatalf("line %+v", r.Line)
	}
}
