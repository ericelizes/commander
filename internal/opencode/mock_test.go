package opencode

import (
	"context"
	"errors"
	"testing"
)

// Compile-time assertion: MockClient must satisfy Client.
var _ Client = (*MockClient)(nil)

// Compile-time assertion: MockServer must satisfy Server.
var _ Server = (*MockServer)(nil)

// Compile-time assertion: execClient must satisfy Client.
var _ Client = (*execClient)(nil)

func TestMockClient_RecordsCalls(t *testing.T) {
	m := &MockClient{
		RunResponse: &RunResult{SessionID: "ses_mock"},
	}

	got, err := m.Run(context.Background(), "", "operator-code", "hello", RunOptions{Model: "x"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.SessionID != "ses_mock" {
		t.Errorf("SessionID = %q", got.SessionID)
	}
	// Channel should be closed and rangeable.
	for range got.Events {
		t.Errorf("expected empty closed channel")
	}

	if len(m.RunCalls) != 1 {
		t.Fatalf("RunCalls = %d, want 1", len(m.RunCalls))
	}
	if m.RunCalls[0].Agent != "operator-code" {
		t.Errorf("Agent = %q", m.RunCalls[0].Agent)
	}
	if m.RunCalls[0].Opts.Model != "x" {
		t.Errorf("Opts.Model = %q", m.RunCalls[0].Opts.Model)
	}
}

func TestMockClient_RunFunc(t *testing.T) {
	want := errors.New("boom")
	m := &MockClient{
		RunFunc: func(ctx context.Context, sessionID, agent, prompt string, opts RunOptions) (*RunResult, error) {
			return nil, want
		},
	}
	if _, err := m.Run(context.Background(), "", "", "", RunOptions{}); !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}

func TestMockClient_StatsAndServe(t *testing.T) {
	m := &MockClient{
		StatsResponse: &Stats{Raw: "ok"},
		ServeResponse: &MockServer{AddrValue: "http://localhost:9997"},
	}

	s, err := m.Stats(context.Background(), "p", 3)
	if err != nil || s.Raw != "ok" {
		t.Errorf("Stats: %v %+v", err, s)
	}
	srv, err := m.Serve(context.Background(), 9997)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if srv.Addr() != "http://localhost:9997" {
		t.Errorf("Addr = %q", srv.Addr())
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	if got := srv.(*MockServer).Shutdowns; got != 1 {
		t.Errorf("Shutdowns = %d", got)
	}
}
