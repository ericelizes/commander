package place

import (
	"context"
	"testing"

	"github.com/example-user/commander/internal/store"
)

func TestPlaceExitReturnsPersistenceFailure(t *testing.T) {
	h := newHarness(t)
	if err := h.st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Exit(context.Background(), &Run{AgentRun: store.AgentRun{ID: h.runID}}, "exited", ""); err == nil {
		t.Fatal("acknowledged exit without persisting its result")
	}
}
