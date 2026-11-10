package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/place"
)

// The place comment path and HTTP writes share applyDoor. Extracting it must
// retain the core intent's live-owner check, holder receipts and pending patch.
func TestPlaceIntegrationDoorIntentCustody(t *testing.T) {
	for _, actor := range []string{"manager-area", ""} {
		t.Run("actor="+actor, func(t *testing.T) {
			t.Setenv("COMMANDER_HOME", t.TempDir())
			t.Setenv(DoorChecksEnv, "on")
			m := &beads.MemClient{EdgeParentsOnly: true}
			m.Seed(beads.Bead{ID: "area", Title: "Area", Labels: []string{"kind:objective"}})
			m.Seed(beads.Bead{ID: "first", Title: "First", Status: "in_progress", Labels: []string{"waiting:1:ci:7"}})
			m.Seed(beads.Bead{ID: "next", Title: "Next", Labels: []string{"owner:operator-next"}})
			m.Edges = []beads.Dep{{IssueID: "first", DependsOnID: "area", Type: "parent-child"}, {IssueID: "next", DependsOnID: "area", Type: "parent-child"}, {IssueID: "next", DependsOnID: "first", Type: "blocks"}}
			var ran [][]string
			liveReads := 0
			s := New(Options{Beads: m, Places: place.NewService(place.Deps{}), LiveOwner: func(owner string) (string, bool) {
				if owner != "operator-next" {
					t.Errorf("owner %q", owner)
				}
				liveReads++
				return "", true
			}, Write: func(_ context.Context, _ string, args []string) ([]byte, []byte, error) {
				ran = append(ran, args)
				return nil, nil, nil
			}})
			req := beads.DoorRequest{Dir: projectDir(t), Args: []string{"close", "first"}, Intent: &beads.DoorIntent{Bead: "first", Verb: "closing first"}}
			if actor != "" {
				req.Env = []string{"COMMANDER_ROLE=" + actor}
			}
			status, out := s.applyDoor(context.Background(), req)
			if status != http.StatusOK || liveReads == 0 {
				t.Fatalf("status %d live reads %d: %+v", status, liveReads, out)
			}
			if actor != "" {
				if out.Exit == 0 || !strings.Contains(out.Error, "no holder") || len(ran) != 0 {
					t.Fatalf("agent escaped custody: %+v ran %v", out, ran)
				}
				return
			}
			if out.Exit != 0 || out.Holders["next"] != "wait" || !out.Settled {
				t.Fatalf("user custody: %+v", out)
			}
			if len(ran) != 2 || ran[0][0] != "update" || ran[0][1] != "next" || !strings.Contains(ran[0][3], "Manager_20to_20start") || ran[1][0] != "close" {
				t.Fatalf("holder must precede close: %v", ran)
			}
			if s.pending["first"].patch.Status != "closed" || len(s.pending["next"].patch.Labels) == 0 {
				t.Fatalf("intent result lost pending board patches: %+v", s.pending)
			}
		})
	}
}
