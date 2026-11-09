package agentmail

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestMockClient_SatisfiesInterface is a runtime echo of the compile-time
// `var _ Client = (*MockClient)(nil)` guard in mock.go.
func TestMockClient_SatisfiesInterface(t *testing.T) {
	var _ Client = (*MockClient)(nil)
	var c Client = &MockClient{}
	if c == nil {
		t.Fatal("MockClient should be assignable to Client")
	}
}

func TestMockClient_RecordsAndReplays(t *testing.T) {
	want := []Mail{{ID: "m1", Subject: "s1"}, {ID: "m2", Subject: "s2"}}
	wantAgents := []Agent{{Name: "commander", Program: "opencode-ai", Model: "sonnet"}}

	mock := &MockClient{
		InboxResult:      want,
		CheckInboxResult: want,
		ListAgentsResult: wantAgents,
	}

	ctx := context.Background()

	if err := mock.Register(ctx, "p", "opencode-ai", "sonnet", "agent-x"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := mock.Send(ctx, "p", "a", "b", "subj", "body", SendOptions{Importance: "high", AckRequired: true}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got, err := mock.Inbox(ctx, "p", "a", time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC), InboxOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(got) != 2 || got[0].ID != "m1" {
		t.Errorf("Inbox replay = %+v", got)
	}

	gotCheck, err := mock.CheckInbox(ctx, "p", "a")
	if err != nil {
		t.Fatalf("CheckInbox: %v", err)
	}
	if len(gotCheck) != 2 {
		t.Errorf("CheckInbox replay = %+v", gotCheck)
	}

	gotAgents, err := mock.ListAgents(ctx, "p")
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(gotAgents) != 1 || gotAgents[0].Name != "commander" {
		t.Errorf("ListAgents replay = %+v", gotAgents)
	}

	if len(mock.Calls) != 5 {
		t.Errorf("recorded %d calls, want 5: %v", len(mock.Calls), mock.Calls)
	}
}

func TestMockClient_PropagatesErrors(t *testing.T) {
	wantErr := errors.New("boom")
	mock := &MockClient{
		RegisterErr:   wantErr,
		SendErr:       wantErr,
		InboxErr:      wantErr,
		CheckInboxErr: wantErr,
		ListAgentsErr: wantErr,
	}
	ctx := context.Background()

	if err := mock.Register(ctx, "p", "opencode-ai", "m", "n"); !errors.Is(err, wantErr) {
		t.Errorf("Register err = %v, want %v", err, wantErr)
	}
	if err := mock.Send(ctx, "p", "a", "b", "s", "x", SendOptions{}); !errors.Is(err, wantErr) {
		t.Errorf("Send err = %v, want %v", err, wantErr)
	}
	if _, err := mock.Inbox(ctx, "p", "a", time.Now(), InboxOptions{}); !errors.Is(err, wantErr) {
		t.Errorf("Inbox err = %v, want %v", err, wantErr)
	}
	if _, err := mock.CheckInbox(ctx, "p", "a"); !errors.Is(err, wantErr) {
		t.Errorf("CheckInbox err = %v, want %v", err, wantErr)
	}
	if _, err := mock.ListAgents(ctx, "p"); !errors.Is(err, wantErr) {
		t.Errorf("ListAgents err = %v, want %v", err, wantErr)
	}
}
