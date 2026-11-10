package store

import (
	"context"
	"errors"
	"testing"
)

func TestPlacesPairRevokeRepair(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	p := Place{Name: "mac:laptop", Kind: "mac", PublicKey: "k1", PairedAt: 10, PairedBy: "user"}
	if err := s.AddPlace(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := s.AddPlace(ctx, p); !errors.Is(err, ErrPlaceExists) {
		t.Fatalf("second pairing: %v", err)
	}
	if ok, err := s.RevokePlace(ctx, "mac:laptop", 20); !ok || err != nil {
		t.Fatalf("revoke: %v %v", ok, err)
	}
	if ok, _ := s.RevokePlace(ctx, "mac:laptop", 21); ok {
		t.Fatal("revoked twice")
	}
	got, _ := s.GetPlace(ctx, "mac:laptop")
	if got == nil || got.Paired() || got.RevokedAt != 20 {
		t.Fatalf("got %+v", got)
	}
	p.PublicKey, p.PairedAt = "k2", 30
	if err := s.AddPlace(ctx, p); err != nil {
		t.Fatalf("re-pair after revoke: %v", err)
	}
	got, _ = s.GetPlace(ctx, "mac:laptop")
	if !got.Paired() || got.PublicKey != "k2" {
		t.Fatalf("got %+v", got)
	}
	if none, err := s.GetPlace(ctx, "mac:other"); none != nil || err != nil {
		t.Fatalf("unknown: %v %v", none, err)
	}
	list, _ := s.ListPlaces(ctx)
	if len(list) != 1 {
		t.Fatalf("list %+v", list)
	}
}

func TestPlaceRunLifecycle(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pid, _ := seedProjectAndObjective(t, s)
	id, err := s.InsertAgentRun(ctx, AgentRun{ProjectID: pid, TaskBeadID: "t-1", AgentName: "operator-t-1", SpawnTime: 1})
	if err != nil {
		t.Fatal(err)
	}
	r := PlaceRun{RunID: id, Place: "mac:laptop", StepID: "s-1", ManagerRole: "manager-w", BriefSHA: "abc", OfferedAt: 1}
	if err := s.InsertPlaceRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	id2, _ := s.InsertAgentRun(ctx, AgentRun{ProjectID: pid, TaskBeadID: "t-2", AgentName: "operator-t-2", SpawnTime: 2})
	if err := s.InsertPlaceRun(ctx, PlaceRun{RunID: id2, Place: "mac:laptop", StepID: "s-2", ManagerRole: "m", BriefSHA: "x", OfferedAt: 2}); !errors.Is(err, ErrPlaceBusy) {
		t.Fatalf("second run on the place: %v", err)
	}
	if ok, _ := s.BeatPlaceRun(ctx, id, 3); ok {
		t.Fatal("beat on an offered run")
	}
	if ok, err := s.AcceptPlaceRun(ctx, id, "hash", 4); !ok || err != nil {
		t.Fatalf("accept: %v %v", ok, err)
	}
	if ok, _ := s.AcceptPlaceRun(ctx, id, "hash2", 5); ok {
		t.Fatal("accepted twice")
	}
	if ok, _ := s.BeatPlaceRun(ctx, id, 6); !ok {
		t.Fatal("beat refused")
	}
	if ok, err := s.EndPlaceRun(ctx, id, PlaceStopped, "usage_limit", 7); !ok || err != nil {
		t.Fatalf("end: %v %v", ok, err)
	}
	if ok, _ := s.EndPlaceRun(ctx, id, PlaceDied, "", 8); ok {
		t.Fatal("ended twice")
	}
	got, _ := s.GetPlaceRun(ctx, id)
	if got.State != PlaceStopped || got.Reason != "usage_limit" || got.TokenHash != "" || got.LastBeat != 6 || got.EndedAt != 7 {
		t.Fatalf("got %+v", got)
	}
	run, _ := s.GetAgentRun(ctx, id)
	if run.Status != "stopped" {
		t.Fatalf("agent run status %q", run.Status)
	}
	if active, _ := s.ListPlaceRuns(ctx, "mac:laptop", true); len(active) != 0 {
		t.Fatalf("active %+v", active)
	}
	if ok, _ := s.RecordPlaceComment(ctx, id, "c1", 9); !ok {
		t.Fatal("first comment")
	}
	if ok, _ := s.RecordPlaceComment(ctx, id, "c1", 10); ok {
		t.Fatal("retried comment recorded twice")
	}
	if err := s.MarkPlaceRun(ctx, id, "settled", true); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkPlaceRun(ctx, id, "state", true); err == nil {
		t.Fatal("unknown flag accepted")
	}
}

func TestInsertPlaceOfferIsOneAtATime(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pid, _ := seedProjectAndObjective(t, s)
	id, err := s.InsertPlaceOffer(ctx, AgentRun{ProjectID: pid, TaskBeadID: "t-1", AgentName: "operator-a", SpawnTime: 5},
		PlaceRun{Place: "mac:laptop", StepID: "s", ManagerRole: "m", BriefSHA: "b", OfferedAt: 5})
	if err != nil {
		t.Fatal(err)
	}
	run, _ := s.GetAgentRun(ctx, id)
	pr, _ := s.GetPlaceRun(ctx, id)
	if run.Status != "running" || run.TmuxPaneID.Valid || pr.State != PlaceOffered {
		t.Fatalf("%+v %+v", run, pr)
	}
	if _, err := s.InsertPlaceOffer(ctx, AgentRun{ProjectID: pid, TaskBeadID: "t-2", AgentName: "operator-b", SpawnTime: 6},
		PlaceRun{Place: "mac:laptop", StepID: "s", ManagerRole: "m", BriefSHA: "b", OfferedAt: 6}); !errors.Is(err, ErrPlaceBusy) {
		t.Fatalf("second offer: %v", err)
	}
	if runs, _ := s.ListAgentRunsByProject(ctx, pid, "running"); len(runs) != 1 {
		t.Fatalf("a refused offer left a run row: %+v", runs)
	}
}
