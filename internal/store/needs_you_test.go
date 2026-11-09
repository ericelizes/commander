package store

import (
	"context"
	"strings"
	"testing"
)

func TestValidateAsk(t *testing.T) {
	for _, ok := range []string{"Run 4 staging cmds", "Approve: import rollout", "Go/hold first Operator"} {
		if err := ValidateAsk(ok); err != nil {
			t.Errorf("%q should be fine: %v", ok, err)
		}
	}
	if err := ValidateAsk("   "); err == nil {
		t.Error("blank ask must be refused")
	}
	if err := ValidateAsk(strings.Repeat("x", MaxAskLen+1)); err == nil || !strings.Contains(err.Error(), "--note") {
		t.Errorf("an over-long ask points at --note: %v", err)
	}
}

func TestNeedsYouRoundTrip(t *testing.T) {
	st := openMem(t)
	ctx := context.Background()
	if err := st.UpsertNeedsYou(ctx, NeedsYou{ProjectID: 2, BeadID: "p-1.3", Ask: " Fix uv login ", Note: "run `uv auth login`"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetNeedsYou(ctx, 2, "p-1.3")
	if err != nil || got == nil || got.Ask != "Fix uv login" || got.Note == "" || got.Since == 0 {
		t.Fatalf("got %+v err %v", got, err)
	}
	// Re-flagging replaces the ask but keeps when it was first flagged.
	if err := st.UpsertNeedsYou(ctx, NeedsYou{ProjectID: 2, BeadID: "p-1.3", Ask: "Approve: rollout", Note: "n2", Since: got.Since + 999}); err != nil {
		t.Fatal(err)
	}
	again, _ := st.GetNeedsYou(ctx, 2, "p-1.3")
	if again.Ask != "Approve: rollout" || again.Since != got.Since {
		t.Fatalf("again %+v", again)
	}
	all, _ := st.ListNeedsYou(ctx)
	if len(all) != 1 {
		t.Fatalf("list = %+v", all)
	}
	if err := st.DeleteNeedsYou(ctx, 2, "p-1.3"); err != nil {
		t.Fatal(err)
	}
	if gone, _ := st.GetNeedsYou(ctx, 2, "p-1.3"); gone != nil {
		t.Fatalf("deleted row still there: %+v", gone)
	}
}
