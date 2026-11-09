package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// holdBeads is a tiny bead tree: labels, comments and parents by id.
type holdBeads struct {
	beads.Client
	labels   map[string][]string
	comments map[string][]beads.Comment
	parents  map[string]string
	showErr  error
	reads    int
	added    []string
	set      []string
}

func (h *holdBeads) Show(_ context.Context, _, id string) (*beads.Bead, error) {
	h.reads++
	if h.showErr != nil {
		return nil, h.showErr
	}
	return &beads.Bead{ID: id, Labels: h.labels[id]}, nil
}

func (h *holdBeads) Details(_ context.Context, _ string, ids []string) (map[string]beads.Detail, error) {
	out := map[string]beads.Detail{}
	for _, id := range ids {
		out[id] = beads.Detail{Parent: h.parents[id], Comments: h.comments[id]}
	}
	return out, nil
}

func (h *holdBeads) SetLabel(_ context.Context, _, id, label string, on bool) error {
	h.set = append(h.set, id+":"+label+":"+map[bool]string{true: "on", false: "off"}[on])
	return nil
}

func (h *holdBeads) AddComment(_ context.Context, _, id, text string) error {
	h.added = append(h.added, text)
	return nil
}

func TestShipHoldTaskAndAncestor(t *testing.T) {
	h := &holdBeads{
		labels:   map[string][]string{"ws.1": {"hold"}},
		comments: map[string][]beads.Comment{"ws.1": {{Text: "hold by manager-ws: wait for review"}}},
		parents:  map[string]string{"ws.1.2": "ws.1", "ws.1": "ws"},
	}
	err := checkTaskHold(context.Background(), h, "d", "ws.1.2", 0)
	if err == nil || !strings.Contains(err.Error(), "held by manager-ws: wait for review") || !strings.Contains(err.Error(), "--no-hold") {
		t.Fatalf("err = %v", err)
	}
}

func TestShipHoldUnheldTaskPasses(t *testing.T) {
	h := &holdBeads{parents: map[string]string{"ws.1": "ws"}}
	if err := checkTaskHold(context.Background(), h, "d", "ws.1", 0); err != nil {
		t.Fatal(err)
	}
}

func TestShipHoldUnreadableBrRefusesAfterRetries(t *testing.T) {
	h := &holdBeads{showErr: errors.New("br busy")}
	err := checkTaskHold(context.Background(), h, "d", "ws.1", time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "treated as held") {
		t.Fatalf("err = %v", err)
	}
	if h.reads != 3 {
		t.Errorf("reads = %d, want 3 attempts", h.reads)
	}
}

func TestShipHoldSetAndClear(t *testing.T) {
	t.Setenv("COMMANDER_ROLE", "manager-ws")
	h := &holdBeads{}
	if err := applyHold(context.Background(), h, "d", "ws.1", "wait", false); err != nil {
		t.Fatal(err)
	}
	if err := applyHold(context.Background(), h, "d", "ws.1", "", true); err != nil {
		t.Fatal(err)
	}
	if strings.Join(h.set, ",") != "ws.1:hold:on,ws.1:hold:off" {
		t.Errorf("labels = %v", h.set)
	}
	if h.added[0] != "hold by manager-ws: wait" || h.added[1] != "hold cleared by manager-ws" {
		t.Errorf("comments = %v", h.added)
	}
}

func TestShipHoldOperatorCannotHoldOrClear(t *testing.T) {
	t.Setenv("COMMANDER_ROLE", "operator-x")
	if err := refuseOperatorHold(true); err == nil {
		t.Error("operator allowed to touch a hold")
	}
	if err := refuseOperatorHold(false); err != nil {
		t.Error(err)
	}
	t.Setenv("COMMANDER_ROLE", "manager-ws")
	if err := refuseOperatorHold(true); err != nil {
		t.Error(err)
	}
}

func TestShipHoldLookupRunTask(t *testing.T) {
	calls := 0
	boom := errors.New("db locked")
	_, err := lookupRunTask(context.Background(), func(context.Context, string) (*store.AgentRun, error) {
		calls++
		return nil, boom
	}, "operator-x", time.Millisecond)
	if !errors.Is(err, boom) || calls != 3 {
		t.Fatalf("lookup error must retry twice then refuse: err=%v calls=%d", err, calls)
	}

	task, err := lookupRunTask(context.Background(), func(context.Context, string) (*store.AgentRun, error) {
		return nil, nil
	}, "operator-x", time.Millisecond)
	if err != nil || task != "" {
		t.Fatalf("unmapped branch must ship: %q %v", task, err)
	}

	calls = 0
	task, err = lookupRunTask(context.Background(), func(context.Context, string) (*store.AgentRun, error) {
		calls++
		if calls < 2 {
			return nil, boom
		}
		return &store.AgentRun{TaskBeadID: "t-1"}, nil
	}, "operator-x", time.Millisecond)
	if err != nil || task != "t-1" {
		t.Fatalf("retry should recover: %q %v", task, err)
	}
}
