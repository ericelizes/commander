package docker

import (
	"context"
	"testing"
)

func TestListByLabel_Argv(t *testing.T) {
	r := &recorder{stdout: []byte("")}
	c := &cmdClient{exec: r.fn(), OS: "linux"}

	got, err := c.ListByLabel(context.Background(), "commander.project")
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	if got == nil {
		t.Error("expected empty (non-nil) slice")
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice; got %d", len(got))
	}

	want := []string{"docker", "ps", "--filter", "label=commander.project", "--format", "json"}
	if len(r.args) != len(want) {
		t.Fatalf("argv = %v; want %v", r.args, want)
	}
	for i, a := range want {
		if r.args[i] != a {
			t.Errorf("argv[%d] = %q; want %q", i, r.args[i], a)
		}
	}
}

func TestListByLabel_LineDelimitedJSON(t *testing.T) {
	// Two containers, line-delimited (NOT a JSON array).
	out := `{"ID":"abc","Names":"cmdr-proj-fb","Image":"img","State":"running","Status":"Up 5 minutes","Labels":"commander.project=proj,commander.task=t1","CreatedAt":"2026-05-09 10:00:00 -0700 PDT"}
{"ID":"def","Names":"cmdr-other-fb","Image":"img","State":"running","Status":"Up 1 minute","Labels":"commander.project=other","CreatedAt":"2026-05-09 11:00:00 -0700 PDT"}
`
	r := &recorder{stdout: []byte(out)}
	c := &cmdClient{exec: r.fn(), OS: "linux"}

	got, err := c.ListByLabel(context.Background(), "commander.project")
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 containers; got %d (%+v)", len(got), got)
	}
	if got[0].ID != "abc" || got[1].ID != "def" {
		t.Errorf("IDs = %q,%q", got[0].ID, got[1].ID)
	}
	if got[0].Status != "running" {
		t.Errorf("Status = %q; want 'running' (from State field)", got[0].Status)
	}
	if got[0].Labels["commander.project"] != "proj" {
		t.Errorf("project label = %q", got[0].Labels["commander.project"])
	}
	if got[0].Labels["commander.task"] != "t1" {
		t.Errorf("task label = %q", got[0].Labels["commander.task"])
	}
}

func TestListByLabel_FallbackToStatusWhenNoState(t *testing.T) {
	out := `{"ID":"abc","Names":"x","Image":"i","Status":"Up 5 minutes","Labels":""}` + "\n"
	r := &recorder{stdout: []byte(out)}
	c := &cmdClient{exec: r.fn(), OS: "linux"}

	got, err := c.ListByLabel(context.Background(), "k")
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	if got[0].Status != "Up 5 minutes" {
		t.Errorf("Status = %q; want 'Up 5 minutes'", got[0].Status)
	}
	// Empty Labels string → empty (non-nil) map.
	if got[0].Labels == nil {
		t.Error("Labels should be non-nil map even when empty")
	}
}

func TestListByLabel_EmptyLabelKey(t *testing.T) {
	c := &cmdClient{exec: (&recorder{}).fn(), OS: "linux"}
	if _, err := c.ListByLabel(context.Background(), ""); err == nil {
		t.Error("expected error for empty label key")
	}
}

func TestListByLabel_BadJSON(t *testing.T) {
	r := &recorder{stdout: []byte("not json\n")}
	c := &cmdClient{exec: r.fn(), OS: "linux"}
	if _, err := c.ListByLabel(context.Background(), "k"); err == nil {
		t.Error("expected parse error")
	}
}

func TestParseLabelString(t *testing.T) {
	got := parseLabelString("a=1,b=2,c=")
	if got["a"] != "1" || got["b"] != "2" || got["c"] != "" {
		t.Errorf("parseLabelString = %v", got)
	}
}
