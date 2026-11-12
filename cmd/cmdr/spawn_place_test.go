package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/place"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
)

// placeStep serves step ms-1.2 labelled to run on mac:laptop.
func placeStep(m *beads.MockClient) *beads.MockClient {
	if m.ShowResults == nil {
		m.ShowResults = map[string]*beads.Bead{}
	}
	m.ShowResults["ms-1.2"] = &beads.Bead{ID: "ms-1.2", Status: "open", Labels: []string{"kind:objective", place.Label("mac:laptop")}}
	return m
}

// stubPlaceOffer records offers (or fails them with err).
func stubPlaceOffer(t *testing.T, sp *fakeOperatorSpawner, err error) *[]place.OfferRequest {
	t.Helper()
	stubSpawnDeps(t, sp, 0, 3)
	var got []place.OfferRequest
	inner := spawnOperatorDeps
	spawnOperatorDeps = func(p string) (*operatorSpawnDeps, error) {
		d, e := inner(p)
		if e != nil {
			return nil, e
		}
		d.OfferPlace = func(_ context.Context, req place.OfferRequest) (*spawn.SpawnResult, error) {
			if err != nil {
				return nil, err
			}
			got = append(got, req)
			return &spawn.SpawnResult{RunID: 9, AgentName: req.AgentName, Runtime: "claude", Account: req.Place, PlaceOfferSent: true}, nil
		}
		return d, nil
	}
	return &got
}

func TestSpawnOperator_OnAPlaceOffersInsteadOfStarting(t *testing.T) {
	sp := &fakeOperatorSpawner{}
	offers := stubPlaceOffer(t, sp, nil)
	mock := placeStep(&beads.MockClient{CreateID: "ms-1.2.1"})
	useMockBeads(t, mock)
	stubMail(t)
	t.Setenv("COMMANDER_WORKFLOW_GATE", "enforce")

	stdout, stderr, readOutput := withTempOutputFiles(t)
	code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", "ms-1.2", "--title", "Summarize reports", "--prompt", "read them"}, stdout, stderr)
	out, errOut := readOutput()
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	if len(sp.reqs) != 0 {
		t.Fatal("a host Operator was started for a place step")
	}
	if len(*offers) != 1 {
		t.Fatalf("offers %+v", *offers)
	}
	o := (*offers)[0]
	if o.Place != "mac:laptop" || o.TaskID != "ms-1.2.1" || o.StepID != "ms-1.2" || o.Brief != "read them" || !strings.HasPrefix(o.AgentName, "operator-") {
		t.Fatalf("offer %+v", o)
	}
	if !strings.Contains(out, `"place":"mac:laptop"`) || !strings.Contains(out, `"offer_sent":true`) {
		t.Fatalf("result %s", out)
	}
	calls := strings.Join(mock.Calls, "\n")
	if !strings.Contains(calls, "owner:") || !strings.Contains(calls, "in_progress") {
		t.Fatalf("task not marked: %v", mock.Calls)
	}
}

func TestSpawnOperator_PlaceRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		args    []string
		openWF  bool
		offer   error
		wantErr string
	}{
		"codex":           {args: []string{"--runtime", "codex"}, wantErr: "Claude only"},
		"writes-workflow": {args: []string{"--writes-workflow"}, wantErr: "approved workflow"},
		"place mismatch":  {args: []string{"--place", "mac:other"}, wantErr: "does not match"},
		"workflow open":   {openWF: true, wantErr: "closed user gate"},
		"place busy":      {offer: errors.New("mac:laptop already has a run offered or running"), wantErr: "already has a run"},
	} {
		t.Run(name, func(t *testing.T) {
			sp := &fakeOperatorSpawner{}
			offers := stubPlaceOffer(t, sp, tc.offer)
			mock := placeStep(&beads.MockClient{CreateID: "ms-1.2.1"})
			if tc.openWF {
				mock.ListResult = append(mock.ListResult, beads.Bead{ID: "ms-1.99", Title: "Workflow approved", Status: "open", Labels: []string{beads.GateLabel}})
			}
			useMockBeads(t, mock)
			stubMail(t)
			t.Setenv("COMMANDER_WORKFLOW_GATE", "enforce")
			stdout, stderr, readOutput := withTempOutputFiles(t)
			args := append([]string{"--project", "manifest", "--step", "ms-1.2", "--title", "t", "--prompt", "p"}, tc.args...)
			code := runInternalSpawnOperator(args, stdout, stderr)
			_, errOut := readOutput()
			if code == 0 || !strings.Contains(errOut, tc.wantErr) {
				t.Fatalf("exit=%d stderr=%s, want %q", code, errOut, tc.wantErr)
			}
			if len(sp.reqs) != 0 || len(*offers) != 0 {
				t.Fatal("something started")
			}
			if tc.offer == nil && strings.Contains(strings.Join(mock.Calls, "\n"), "Create:") {
				t.Fatalf("a task was created before the refusal: %v", mock.Calls)
			}
		})
	}
}

func TestPlaceSpawnRefusal(t *testing.T) {
	if err := placeSpawnRefusal("mac:laptop", "", "", "x", false); err == nil || !strings.Contains(err.Error(), "own Claude login") {
		t.Fatal(err)
	}
	if err := placeSpawnRefusal("", "mac:laptop", "", "", false); err == nil || !strings.Contains(err.Error(), "no run-on label") {
		t.Fatal(err)
	}
	if err := placeSpawnRefusal("", "", "codex", "x", true); err != nil {
		t.Fatal("a host spawn judged as a place spawn")
	}
}

func TestStopPlaceRuns(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	pid, _ := st.InsertProject(ctx, store.Project{Name: "demo", Type: "code", Path: t.TempDir(), Status: "active"})
	id, _ := st.InsertPlaceOffer(ctx, store.AgentRun{ProjectID: pid, TaskBeadID: "d-1.1", AgentName: "operator-mac-1", SpawnTime: 1},
		store.PlaceRun{Place: "mac:laptop", StepID: "d-1", ManagerRole: "manager-d-1", BriefSHA: "x", OfferedAt: 1})
	var posted []int64
	old := postPlaceStop
	defer func() { postPlaceStop = old }()
	postPlaceStop = func(_ context.Context, _ string, runID int64, by string) error {
		posted = append(posted, runID)
		return nil
	}
	errBuf := &strings.Builder{}
	stopped, rest := stopPlaceRuns(ctx, t.TempDir(), st, []string{"operator-mac-1", "manager-d-1"}, "manager-d-1", errBuf)
	if len(stopped) != 1 || len(rest) != 1 || rest[0] != "manager-d-1" || len(posted) != 1 || posted[0] != id {
		t.Fatalf("stopped %v rest %v posted %v", stopped, rest, posted)
	}
	t.Setenv("COMMANDER_ROLE", "operator-other")
	posted = nil
	if stopped, _ := stopPlaceRuns(ctx, t.TempDir(), st, []string{"operator-mac-1"}, "operator-other", errBuf); len(stopped) != 0 || len(posted) != 0 {
		t.Fatal("an Operator stopped a place run")
	}
}
