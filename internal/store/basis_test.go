package store

import (
	"context"
	"testing"
)

func TestBasisRoundTripAndReRaise(t *testing.T) {
	st := openMem(t)
	ctx := context.Background()
	card := `{"question":"Run it?"}`
	up := func(spec string) {
		t.Helper()
		n := NeedsYou{ProjectID: 1, BeadID: "p-1", Ask: "Run", Decision: card}
		if spec != "" {
			n.Basis = &Basis{Spec: spec, Named: map[string]string{"p-2": "open"}}
		}
		if err := st.UpsertNeedsYou(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	up("aaa")
	got, _ := st.GetNeedsYou(ctx, 1, "p-1")
	if got.Basis == nil || got.Basis.Spec != "aaa" || got.Basis.Gen != 1 || got.Basis.Raised == 0 || got.Basis.Named["p-2"] != "open" {
		t.Fatalf("basis: %+v", got.Basis)
	}
	up("aaa") // same card, same spec: no new version
	if ps, _ := st.ListPitches(ctx, 1, "p-1"); len(ps) != 1 {
		t.Fatalf("versions: %d", len(ps))
	}
	up("bbb") // the same card re-raised against an edited spec: a new version with the new basis
	ps, _ := st.ListPitches(ctx, 1, "p-1")
	if len(ps) != 2 {
		t.Fatalf("versions: %d", len(ps))
	}
	list, _ := st.ListNeedsYou(ctx)
	if len(list) != 1 || list[0].Basis == nil || list[0].Basis.Spec != "bbb" || list[0].Basis.Gen != 2 {
		t.Fatalf("list basis: %+v", list)
	}
	if err := st.RebaselineCard(ctx, 1, "p-1", Basis{Spec: "ccc", AgeChecks: 1}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetNeedsYou(ctx, 1, "p-1")
	if got.Basis.Spec != "ccc" || got.Basis.Gen != 2 || got.Basis.AgeChecks != 1 {
		t.Fatalf("re-baselined in place: %+v", got.Basis)
	}
	if ps, _ := st.ListPitches(ctx, 1, "p-1"); len(ps) != 2 {
		t.Fatalf("re-baseline adds no version: %d", len(ps))
	}
}

func TestCardRaisedBeforeTheMigrationHasNoBasis(t *testing.T) {
	st := openMem(t)
	ctx := context.Background()
	if err := st.UpsertNeedsYou(ctx, NeedsYou{ProjectID: 1, BeadID: "p-1", Ask: "Run", Decision: `{"question":"Q"}`}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetNeedsYou(ctx, 1, "p-1")
	if got.Basis != nil {
		t.Fatalf("no basis expected: %+v", got.Basis)
	}
}

func TestBasisDependencyReraiseAndEmptyMaps(t *testing.T) {
	st := openMem(t)
	ctx := context.Background()
	b := &Basis{Spec: "same", Named: map[string]string{}, PRs: map[string]string{}, Answers: map[string]int{}}
	up := func() {
		t.Helper()
		if err := st.RecordPitch(ctx, 1, "p-1", "Q", `{"question":"Q"}`, b); err != nil {
			t.Fatal(err)
		}
	}
	up()
	up()
	if ps, _ := st.ListPitches(ctx, 1, "p-1"); len(ps) != 1 {
		t.Fatal("empty maps made a new generation")
	}
	b.PRs["owner/repo#12"] = "OPEN"
	up()
	b.PRs["owner/repo#12"] = "MERGED"
	up()
	b.Answers["p-1"] = 2
	up()
	if ps, _ := st.ListPitches(ctx, 1, "p-1"); len(ps) != 4 {
		t.Fatalf("versions=%d", len(ps))
	}
}
