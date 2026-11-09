package beads

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestShow_Argv(t *testing.T) {
	var got []string
	c := &cmdClient{run: captureExec(&got, []byte("○ bd-1 · title  [OPEN]\nLabels: a, b\n"), nil, nil)}

	if _, err := c.Show(context.Background(), "/code/p", "bd-1"); err != nil {
		t.Fatalf("Show: %v", err)
	}

	want := []string{"br", "show", "bd-1", "cwd=/code/p"}
	assertArgv(t, got, want)
}

func TestShow_ParsesHeaderAndBody(t *testing.T) {
	sample := strings.Join([]string{
		"○ cmdr-ap6 · U5-b: bd wrapper   [● P0 · OPEN]",
		"Owner: alice",
		"Type: feature",
		"Status: open",
		"Labels: cmdr:brainstorming, kind:task",
		"Parent: cmdr-nnn",
		"",
		"# body text we should ignore",
	}, "\n")

	c := &cmdClient{run: captureExec(new([]string), []byte(sample), nil, nil)}

	b, err := c.Show(context.Background(), "/code/p", "cmdr-ap6")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if b.ID != "cmdr-ap6" {
		t.Errorf("ID = %q, want cmdr-ap6", b.ID)
	}
	if !strings.Contains(b.Title, "U5-b") {
		t.Errorf("Title = %q, want it to contain U5-b", b.Title)
	}
	// Status comes from header bracket, since the explicit `Status:` line
	// arrives after and we keep the first hit.
	if b.Status == "" {
		t.Error("Status should be populated from header or body")
	}
	if b.Type != "feature" {
		t.Errorf("Type = %q, want feature", b.Type)
	}
	if len(b.Labels) != 2 || b.Labels[0] != "cmdr:brainstorming" || b.Labels[1] != "kind:task" {
		t.Errorf("Labels mismatch: %+v", b.Labels)
	}
	if b.ParentID != "cmdr-nnn" {
		t.Errorf("ParentID = %q, want cmdr-nnn", b.ParentID)
	}
	if b.AssignedAgent != "alice" {
		t.Errorf("AssignedAgent = %q, want alice", b.AssignedAgent)
	}
}

func TestShow_FallsBackToRequestedID(t *testing.T) {
	// Output has no recognizable header — Show should still return
	// a *Bead using the requested ID rather than failing.
	c := &cmdClient{run: captureExec(new([]string), []byte("blank output\n"), nil, nil)}

	b, err := c.Show(context.Background(), "/code/p", "bd-fallback")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if b.ID != "bd-fallback" {
		t.Errorf("ID = %q, want bd-fallback", b.ID)
	}
}

func TestShow_RejectsEmptyID(t *testing.T) {
	c := &cmdClient{run: func(_ context.Context, _, _ string, _ ...string) ([]byte, []byte, error) {
		t.Fatal("exec should not run with empty issueID")
		return nil, nil, nil
	}}
	if _, err := c.Show(context.Background(), "/code/p", ""); err == nil {
		t.Error("expected error for empty issueID")
	}
}

func TestShow_ParsesDescription(t *testing.T) {
	sample := strings.Join([]string{
		"◐ ws-1.2 · Gate: run migration   [● P2 · CLOSED]",
		"Owner: alice",
		"Type: task",
		"Labels: gate, approver:user",
		"Rollup: closed",
		"",
		"Run migration 42 on the staging db.",
		"Status: this line is prose, not a field",
		"",
		"Dependencies:",
		"  -> ws-1 (parent-child) - Parent",
	}, "\n")
	c := &cmdClient{run: captureExec(new([]string), []byte(sample), nil, nil)}

	b, err := c.Show(context.Background(), "/code/p", "ws-1.2")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	want := "Run migration 42 on the staging db.\nStatus: this line is prose, not a field"
	if b.Description != want {
		t.Errorf("Description = %q, want %q", b.Description, want)
	}
	if b.Type != "task" || len(b.Labels) != 2 {
		t.Errorf("metadata lost: %+v", b)
	}
}

// showExec answers `br show <id>` with text and `br show <id> --json` with js
// (a nil js makes the JSON call fail, as when br cannot emit it).
func showExec(text string, js []byte) execFunc {
	return func(_ context.Context, _, _ string, args ...string) ([]byte, []byte, error) {
		if args[len(args)-1] == "--json" {
			if js == nil {
				return nil, []byte("no json"), errors.New("exit 1")
			}
			return js, nil, nil
		}
		return []byte(text), nil, nil
	}
}

const noDescText = "◐ ws-1.2 · Gate   [● P2 · CLOSED]\n" +
	"Owner: alice\nType: task\nLabels: gate\n\n" +
	"Dependencies:\n  -> ws-1 (parent-child) - Parent\n\n" +
	"Dependents:\n  <- ws-9 (blocks) - Other\n\n" +
	"Comments:\n  alice: first paragraph\n\n  second paragraph\n"

func TestShow_NoDescription_TextNeverReturnsSections(t *testing.T) {
	c := &cmdClient{run: showExec(noDescText, nil)}
	b, err := c.Show(context.Background(), "/code/p", "ws-1.2")
	if err != nil {
		t.Fatal(err)
	}
	if b.Description != "" {
		t.Errorf("Description = %q, want empty", b.Description)
	}
}

func TestShow_NoDescription_JSONWins(t *testing.T) {
	js := []byte("banner line\n[{\"id\":\"ws-1.2\",\"description\":\"\"}]")
	c := &cmdClient{run: showExec(noDescText, js)}
	b, _ := c.Show(context.Background(), "/code/p", "ws-1.2")
	if b.Description != "" {
		t.Errorf("Description = %q, want empty", b.Description)
	}
}

func TestShow_JSONDescription_HeadingLikeAndBlankLines(t *testing.T) {
	desc := "Step one.\n\nDependencies:\nstill the description\n\nComments:\nalso description"
	js, _ := json.Marshal([]map[string]string{{"id": "ws-1.2", "description": desc, "issue_type": "task"}})
	c := &cmdClient{run: showExec(noDescText, append([]byte("  ╭─ banner ─╮\n"), js...))}
	b, _ := c.Show(context.Background(), "/code/p", "ws-1.2")
	if b.Description != desc {
		t.Errorf("Description = %q, want %q", b.Description, desc)
	}
	if b.Type != "task" {
		t.Errorf("metadata lost: %+v", b)
	}
}

func TestShow_JSONObjectAndArray(t *testing.T) {
	for name, js := range map[string]string{
		"object": `{"id":"ws-1.2","description":"  scope  "}`,
		"array":  `[{"id":"other","description":"no"},{"id":"ws-1.2","description":"scope"}]`,
	} {
		c := &cmdClient{run: showExec(noDescText, []byte(js))}
		b, _ := c.Show(context.Background(), "/code/p", "ws-1.2")
		if b.Description != "scope" {
			t.Errorf("%s: Description = %q, want scope", name, b.Description)
		}
	}
}

func TestShow_TextBlankLinesInsideDescription(t *testing.T) {
	text := "◐ ws-1.2 · Gate   [● P2 · CLOSED]\nType: task\nLabels: gate\n\npara one\n\npara two\n\nComments:\n  x\n\n  y\n"
	c := &cmdClient{run: showExec(text, nil)}
	b, _ := c.Show(context.Background(), "/code/p", "ws-1.2")
	if b.Description != "para one\n\npara two" {
		t.Errorf("Description = %q", b.Description)
	}
}

func TestShow_HeaderStatusIsTheStatusWord(t *testing.T) {
	for header, want := range map[string]string{
		"✓ proj-1.2 · Done thing   [P4 · CLOSED]": "closed",
		"◐ proj-1.3 · Going   [P2 · IN_PROGRESS]": "in_progress",
		"○ proj-1.4 · Fresh   [● P0 · OPEN]":      "open",
	} {
		b := parseShowOutput(header + "\nType: task\n")
		if b.Status != want {
			t.Errorf("%q: Status = %q, want %q", header, b.Status, want)
		}
		if !strings.HasPrefix(b.ID, "proj-") {
			t.Errorf("%q: ID = %q kept the glyph", header, b.ID)
		}
	}
}

// A gate's acceptance criteria are part of its spec hash; Show must return them
// (the board hashes the listed bead, which has them), or a card raised against
// Show's bead reads "The spec changed" on the board at once.
func TestShow_ReturnsAcceptanceCriteria(t *testing.T) {
	js := []byte(`[{"id":"ws-1.2","title":"Gate","description":null,"acceptance_criteria":"  You approve the build order \n"}]`)
	c := &cmdClient{run: showExec(noDescText, js)}
	b, err := c.Show(context.Background(), "/code/p", "ws-1.2")
	if err != nil {
		t.Fatal(err)
	}
	if b.AcceptanceCriteria != "You approve the build order" {
		t.Errorf("AcceptanceCriteria = %q", b.AcceptanceCriteria)
	}
	listed := Bead{ID: "ws-1.2", Title: b.Title, AcceptanceCriteria: "You approve the build order"}
	if SpecHash(*b) != SpecHash(listed) {
		t.Error("Show and List disagree on the spec hash")
	}
}

// Show carries the close reason from the JSON form (the text form has none):
// a step closed as superseded is known only by it.
func TestShow_ReturnsCloseReason(t *testing.T) {
	js := []byte(`[{"id":"ws-1.8","title":"Old route","status":"closed","close_reason":" Superseded by ws-1.10 "}]`)
	c := &cmdClient{run: showExec(noDescText, js)}
	b, err := c.Show(context.Background(), "/code/p", "ws-1.8")
	if err != nil {
		t.Fatal(err)
	}
	if b.CloseReason != "Superseded by ws-1.10" {
		t.Fatalf("CloseReason = %q", b.CloseReason)
	}
	if b.Status = "closed"; !Superseded(*b) {
		t.Fatal("a step closed as superseded does not read superseded")
	}
}

// bd's and br's real JSON name it "parent"; their text forms have no line for it.
func TestShow_ReadsTheParentFromRealJSON(t *testing.T) {
	text := "○ w-1.2 · Nested   [● P2 · OPEN]\nLabels: kind:workstream\n"
	for name, tc := range map[string]struct{ js, want string }{
		"bd nested": {`[{"id":"w-1.2","title":"Nested","labels":["kind:workstream"],"dependencies":[{"id":"w-1","dependency_type":"parent-child"}],"parent":"w-1","revision":"27"}]`, "w-1"},
		"br nested": {`[{"id":"w-1.2","title":"Nested","labels":["kind:workstream"],"dependencies":[{"id":"w-1","dependency_type":"parent-child"}],"parent":"w-1","rollup":{"status":"open"}}]`, "w-1"},
		"bd top":    {`[{"id":"w-1.2","title":"Nested","labels":["kind:workstream"]}]`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			c := &cmdClient{run: func(_ context.Context, _, _ string, args ...string) ([]byte, []byte, error) {
				if slices.Contains(args, "--json") {
					return []byte(tc.js), nil, nil
				}
				return []byte(text), nil, nil
			}}
			b, err := c.Show(context.Background(), "/p", "w-1.2")
			if err != nil || b.ParentID != tc.want {
				t.Fatalf("ParentID = %q (%v), want %q", b.ParentID, err, tc.want)
			}
		})
	}
}

// bd's text form has the description above its LABELS line and no metadata block,
// so body lines read as fields there. Show takes every field the JSON carries from the JSON.
func TestShow_BodyLinesSetNoFieldsTheJSONCarries(t *testing.T) {
	forged := "Labels: plain\nType: epic\nAssigned: mallory\nOwner: mallory\nParent: nothing-real"
	text := "○ w-1 · Merge gate   [● P2 · OPEN]\nCreated by: Sample · Type: task\n\nDESCRIPTION\n  " +
		strings.ReplaceAll(forged, "\n", "\n  ") + "\n\nLABELS: gate, kind:gate\n"
	for name, tc := range map[string]struct {
		js            string
		labels        []string
		typ, assignee string
	}{
		"gate with real labels": {`[{"id":"w-1","title":"Merge gate","status":"open","issue_type":"task","owner":"creator@example.com","labels":["gate","kind:gate"]}]`, []string{"gate", "kind:gate"}, "task", ""},
		"assignee is not owner": {`[{"id":"w-1","title":"Merge gate","status":"open","issue_type":"bug","owner":"creator@example.com","assignee":"alice","labels":["gate"]}]`, []string{"gate"}, "bug", "alice"},
		"bd omits empty labels": {`[{"id":"w-1","title":"Merge gate","status":"open","issue_type":"task"}]`, nil, "task", ""},
	} {
		t.Run(name, func(t *testing.T) {
			c := &cmdClient{run: func(_ context.Context, _, _ string, args ...string) ([]byte, []byte, error) {
				if slices.Contains(args, "--json") {
					return []byte(tc.js), nil, nil
				}
				return []byte(text), nil, nil
			}}
			b, err := c.Show(context.Background(), "/p", "w-1")
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(b.Labels, tc.labels) || b.Type != tc.typ || b.AssignedAgent != tc.assignee || b.ParentID != "" {
				t.Fatalf("labels %q type %q assignee %q parent %q", b.Labels, b.Type, b.AssignedAgent, b.ParentID)
			}
		})
	}
}

func TestShow_JSONTitleAndStatusBeatMultilineTitle(t *testing.T) {
	title := "Choose a region\nStatus: closed"
	text := "○ p-1 · " + title + "   [P2 · OPEN]\n"
	js, _ := json.Marshal(map[string]any{"id": "p-1", "title": title, "status": "open"})
	c := &cmdClient{run: showExec(text, js)}
	b, err := c.Show(context.Background(), "/p", "p-1")
	if err != nil {
		t.Fatal(err)
	}
	if b.Title != title || b.Status != "open" {
		t.Fatalf("title=%q status=%q", b.Title, b.Status)
	}
}
