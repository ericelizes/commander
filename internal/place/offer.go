package place

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/example-user/commander/internal/store"
)

// OfferRequest is a spawn on a place: the task bead exists, the gates were
// judged by the caller; this records the run for the Mac to pick up.
type OfferRequest struct {
	ProjectID   int64
	TaskID      string
	AgentName   string
	Place       string
	StepID      string
	ManagerRole string
	ProdGate    string
	Brief       string
	Runtime     string
	Now         time.Time
}

// RecordOffer checks the place and records the run (offered). Refused: an
// invalid, unknown or revoked place, a runtime other than Claude, and a place
// that already holds an offered or running run.
func RecordOffer(ctx context.Context, st *store.Store, r OfferRequest) (int64, error) {
	if !ValidName(r.Place) {
		return 0, fmt.Errorf("%q is not a place (mac:<name>)", r.Place)
	}
	if r.Runtime != "" && r.Runtime != "claude" {
		return 0, fmt.Errorf("a place run is Claude only in the first slice (--runtime %s refused)", r.Runtime)
	}
	p, err := st.GetPlace(ctx, r.Place)
	if err != nil {
		return 0, err
	}
	if p == nil {
		return 0, fmt.Errorf("%s is not paired (the user pairs it with cmdr place pair)", r.Place)
	}
	if !p.Paired() {
		return 0, fmt.Errorf("%s was revoked", r.Place)
	}
	if r.Now.IsZero() {
		r.Now = time.Now()
	}
	id, err := st.InsertPlaceOffer(ctx,
		store.AgentRun{ProjectID: r.ProjectID, TaskBeadID: r.TaskID, AgentName: r.AgentName, SpawnTime: r.Now.Unix()},
		store.PlaceRun{Place: r.Place, StepID: r.StepID, ManagerRole: r.ManagerRole, ProdGate: r.ProdGate,
			BriefSHA: BriefSHA(r.Brief), OfferedAt: r.Now.Unix()})
	if errors.Is(err, store.ErrPlaceBusy) {
		return 0, fmt.Errorf("%s already has a run offered or running (one at a time)", r.Place)
	}
	return id, err
}
