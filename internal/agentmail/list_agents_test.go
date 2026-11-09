package agentmail

import (
	"context"
	"strings"
	"testing"
)

func TestListAgents_FullArgv(t *testing.T) {
	var capturedArgs []string
	c := &cmdClient{
		exec: func(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
			capturedArgs = args
			return []byte("[]"), nil, nil
		},
	}

	if _, err := c.ListAgents(context.Background(), "myproj"); err != nil {
		t.Fatalf("ListAgents: %v", err)
	}

	want := []string{"agents", "list", "--project", "myproj", "--json"}
	if strings.Join(capturedArgs, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v\n want = %v", capturedArgs, want)
	}
}

func TestListAgents_ParsesArrayJSON(t *testing.T) {
	c := &cmdClient{
		exec: func(_ context.Context, _ string, _ ...string) ([]byte, []byte, error) {
			return []byte(`[
				{"name":"commander","program":"opencode-ai","model":"sonnet"},
				{"name":"lead-myproj","program":"opencode-ai","model":"sonnet"}
			]`), nil, nil
		},
	}
	got, err := c.ListAgents(context.Background(), "myproj")
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(agents) = %d, want 2", len(got))
	}
	if got[0].Name != "commander" || got[0].Program != "opencode-ai" || got[0].Model != "sonnet" {
		t.Errorf("agents[0] = %+v", got[0])
	}
	if got[1].Name != "lead-myproj" {
		t.Errorf("agents[1].Name = %q, want lead-myproj", got[1].Name)
	}
}

func TestListAgents_ParsesEnvelopeJSON(t *testing.T) {
	c := &cmdClient{
		exec: func(_ context.Context, _ string, _ ...string) ([]byte, []byte, error) {
			return []byte(`{"agents":[{"name":"x","program":"opencode-ai","model":"sonnet"}]}`), nil, nil
		},
	}
	got, err := c.ListAgents(context.Background(), "p")
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(got) != 1 || got[0].Name != "x" {
		t.Errorf("parsed = %+v", got)
	}
}

func TestListAgents_Validation(t *testing.T) {
	c := &cmdClient{exec: func(context.Context, string, ...string) ([]byte, []byte, error) {
		return nil, nil, nil
	}}
	if _, err := c.ListAgents(context.Background(), ""); err == nil {
		t.Error("expected error on empty project")
	}
}
