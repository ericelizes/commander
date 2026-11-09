package livedata

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/example-user/commander/internal/config"
)

var staleDays atomic.Int64

func init() { staleDays.Store(config.DefaultStaleDays) }

// SetStaleDays sets how old a card may get before it reads out of date
// ([needs] stale_days; 0 = the age rule is off).
func SetStaleDays(n int) { staleDays.Store(int64(n)) }

// StaleDays is the age limit in days (0 = off).
func StaleDays() int { return int(staleDays.Load()) }

// Stale reasons on a BoardAsk.
const (
	StalePush = "push"
	StaleSpec = "spec" // the card's bead was edited (title, description or acceptance) after the card was raised
	StaleAge  = "age"  // raised longer ago than [needs] stale_days
)

// CardStale says whether an open card is out of date and why. It compares only
// what the card was raised against: the spec fingerprint of its bead and the
// raise time, plus the explicit push counter. Comments, ordinary labels, waits, wait-checked stamps, the owner and other
// beads are not part of either, so they never make a card stale. A card with no
// recorded basis (raised before it was kept) can only age. A spec change wins
// over age; repeated pushes are surfaced first.
func CardStale(a NeedsYou, now int64) (reason, why string) {
	if why := pushWhy(a.PushCount, a.PushFirst); why != "" {
		return StalePush, why
	}
	raised := a.Since
	if b := a.Basis; b != nil {
		if b.Spec != "" && a.SpecHash != "" && b.Spec != a.SpecHash {
			return StaleSpec, "The spec changed since this was asked"
		}
		if b.Raised > 0 {
			raised = b.Raised
		}
	}
	if days := StaleDays(); days > 0 && raised > 0 && now-raised > int64(days)*86400 {
		return StaleAge, fmt.Sprintf("Asked %d days ago", (now-raised)/86400)
	}
	return "", ""
}

// pushWhy uses the first target, not a comment or the bead's update time.
func pushWhy(count int, first int64) string {
	if count < 2 {
		return ""
	}
	text := fmt.Sprintf("Pushed back %d times", count)
	if first > 0 {
		text += " since " + time.Unix(first, 0).In(BoardZone()).Format("Mon Jan 2")
	}
	return text
}
func withCardWarning(text, why string) string {
	if why != "" {
		return text + " · " + why
	}
	return text
}
func (b board) stepWarning(s SnapStep) string {
	if stepClosed(s) || s.Superseded {
		return ""
	}
	for _, a := range b.s.NeedsYou {
		if a.BeadID == s.BeadID {
			return a.StaleWhy
		}
	}
	return pushWhy(s.PushCount, s.PushFirst)
}

// Include re-baseline time, spec and dependencies: card-check preserves the pitch version.
func StaleNoticeKey(card NeedsYou) string {
	gen, raised, spec := 0, card.Since, ""
	if card.Basis != nil {
		gen, raised, spec = card.Basis.Gen, card.Basis.Raised, card.Basis.Spec
	}
	key := fmt.Sprintf("%s:%s:%d:%d:%s", card.Project, card.BeadID, gen, raised, spec)
	if b := card.Basis; b != nil && (len(b.Named)+len(b.PRs)+len(b.Answers) > 0) {
		raw, _ := json.Marshal(b)
		key += fmt.Sprintf(":%x", sha256.Sum256(raw))
	}
	return key
}
