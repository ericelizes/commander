package agentmail

import (
	"context"
	"errors"
	"testing"
)

func TestLookupExistingAgent(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		want          error
	}{
		{"exact", `{"id":7,"project_id":3,"name":"BlueLake"}`, nil},
		{"absent", "ERR:Agent 'BlueLake' not found in project '/fixture/fleet'", ErrAgentMissing},
		{"absent code", "ERR:AGENT_NOT_FOUND", ErrAgentMissing},
		{"normalized", `{"id":7,"project_id":3,"name":"Bluelake"}`, ErrAgentMissing},
		{"retired", `{"id":7,"project_id":3,"name":"BlueLake","retired_at":"2026-01-01"}`, ErrAgentInactive},
		{"deregistered", `{"id":7,"project_id":3,"name":"BlueLake","deregistered_at":"2026-01-01"}`, ErrAgentInactive},
		{"server error", "ERR:database is busy", ErrWhoisFailed},
		{"project absent", "ERR:Project '/fixture/fleet' not found", ErrWhoisFailed},
		{"unknown tool", "ERR:Unknown tool: whois", ErrWhoisUnsupported},
		{"malformed", `{`, ErrWhoisUnsupported},
		{"array", `[{"id":7,"project_id":3,"name":"BlueLake"}]`, ErrWhoisUnsupported},
		{"missing project", `{"id":7,"name":"BlueLake"}`, ErrWhoisUnsupported},
		{"missing id", `{"project_id":3,"name":"BlueLake"}`, ErrWhoisUnsupported},
		{"missing name", `{"id":7,"project_id":3}`, ErrWhoisUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, seen := fakeServer(t, map[string]string{"whois": tc.payload})
			got, err := NewServerClient(srv.URL, "fixture").LookupExistingAgent(context.Background(), "/fixture/fleet", "BlueLake")
			if !errors.Is(err, tc.want) {
				t.Fatalf("identity=%+v err=%v, want %v", got, err, tc.want)
			}
			args := (*seen)[0].Params.Arguments
			if len(*seen) != 1 || args["project_key"] != "/fixture/fleet" || args["agent_name"] != "BlueLake" || args["include_recent_commits"] != false {
				t.Fatalf("calls=%+v", *seen)
			}
		})
	}
}

func TestLookupExistingAgentUnreachableServer(t *testing.T) {
	srv, _ := fakeServer(t, nil)
	srv.Close()
	_, err := NewServerClient(srv.URL, "fixture").LookupExistingAgent(context.Background(), "/fixture/fleet", "BlueLake")
	if !errors.Is(err, ErrWhoisFailed) {
		t.Fatalf("err=%v", err)
	}
}
