package store

import (
	"context"
	"testing"
)

func TestEveryCardIsAVersionAndKeepsItsAnswer(t *testing.T) {
	st := openMem(t)
	ctx := context.Background()
	card := func(q string) string { return `{"question":"` + q + `"}` }
	for _, q := range []string{"Run it?", "Run it?", "Run it on 20 apps?"} {
		if err := st.UpsertNeedsYou(ctx, NeedsYou{ProjectID: 2, BeadID: "p-1.3.2", Ask: "Greenlight the run", Decision: card(q)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpsertNeedsYou(ctx, NeedsYou{ProjectID: 2, BeadID: "p-1.30", Ask: "Other", Decision: card("Other?")}); err != nil {
		t.Fatal(err)
	}
	if err := st.AnswerPitch(ctx, 2, "p-1.3.2", "Approve"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteNeedsYou(ctx, 2, "p-1.3.2"); err != nil {
		t.Fatal(err)
	}

	got, err := st.ListPitches(ctx, 2, "p-1.3")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("the same card twice is one version, a changed one the next; p-1.30 is not under p-1.3: %+v", got)
	}
	if got[0].Version != 1 || got[1].Version != 2 || got[1].Decision != card("Run it on 20 apps?") {
		t.Fatalf("versions: %+v", got)
	}
	if got[0].Answer != "" || got[1].Answer != "Approve" || got[1].AnsweredAt == 0 {
		t.Fatalf("the answer goes on the version answered: %+v", got)
	}
	all, _ := st.ListPitches(ctx, 2, "")
	if len(all) != 3 {
		t.Fatalf("all pitches: %d", len(all))
	}
}
