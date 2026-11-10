package place

import (
	"fmt"
	"time"

	"github.com/example-user/commander/internal/store"
)

// Liveness thresholds: a run heartbeats every 30s.
const (
	LiveWithin    = 2 * time.Minute  // a beat this recent = live
	DiedAfter     = 15 * time.Minute // no beat this long = died
	OfferTimeout  = 15 * time.Minute // an offer nobody answered (the dialog times out at 10)
	HeartbeatEach = 30 * time.Second
)

// Liveness is what the board says about a place run.
type Liveness string

const (
	Offered     Liveness = "offered"     // waiting for the user's dialog on the Mac
	Live        Liveness = "live"        // beat within LiveWithin
	Unreachable Liveness = "unreachable" // 2-15 min without a beat: not Broken
	Died        Liveness = "died"        // over 15 min: cmdrd settles it
	Ended       Liveness = "ended"
)

// LivenessOf classifies a run at now.
func LivenessOf(r store.PlaceRun, now time.Time) Liveness {
	switch r.State {
	case store.PlaceOffered:
		if now.Sub(time.Unix(r.OfferedAt, 0)) > OfferTimeout {
			return Died
		}
		return Offered
	case store.PlaceRunning:
		quiet := now.Sub(time.Unix(r.LastBeat, 0))
		switch {
		case quiet <= LiveWithin:
			return Live
		case quiet <= DiedAfter:
			return Unreachable
		default:
			return Died
		}
	}
	return Ended
}

// Words is the board's line for a run's Operator ("Operator working on
// mac:laptop", "Operator on mac:laptop unreachable 4 min").
func Words(r store.PlaceRun, now time.Time) string {
	switch LivenessOf(r, now) {
	case Offered:
		return "Waiting for the user to start the run on " + r.Place
	case Live:
		return "Operator working on " + r.Place
	case Unreachable:
		return fmt.Sprintf("Operator on %s unreachable %d min", r.Place, int(now.Sub(time.Unix(r.LastBeat, 0)).Minutes()))
	case Died:
		return "Operator on " + r.Place + " lost"
	}
	return "Operator on " + r.Place + " " + r.State
}
