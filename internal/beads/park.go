package beads

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Parking puts an ask the user put on hold out of play: status deferred (the
// "Deferred = parked" rule), plus labels that remember how to bring it back.
// Nothing else on the bead changes, so a needs-user marker and the stored
// decision card survive and Unpark shows the same card again.
const (
	// ParkedLabel marks a bead parked with Park (a cheap list filter for the
	// minute loop); a bead deferred some other way lacks it.
	ParkedLabel = "parked"
	// ParkedFromPrefix + the status the bead had before parking.
	ParkedFromPrefix = "parked-from:"
	// ParkedUntilPrefix + the unix second cmdrd un-parks it; absent = until unparked by hand.
	ParkedUntilPrefix = "parked-until:"
	// ParkedNotifiedPrefix + the unix second cmdrd mailed that the park is due,
	// so a restart between the mail and the unpark does not mail again.
	ParkedNotifiedPrefix = "parked-notified:"
)

// ParkedUntil is the unix second a parked bead comes back, 0 when it has no time.
func ParkedUntil(labels []string) int64 {
	for _, l := range labels {
		if v, ok := strings.CutPrefix(l, ParkedUntilPrefix); ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				return n
			}
		}
	}
	return 0
}

// ParseParkUntil reads --until: an RFC3339 time or a duration from now (2h, 3d).
func ParseParkUntil(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		if !t.After(now) {
			return time.Time{}, fmt.Errorf("--until %s is in the past", s)
		}
		return t, nil
	}
	if days, ok := strings.CutSuffix(s, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil && n > 0 {
			return now.Add(time.Duration(n) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return now.Add(d), nil
	}
	return time.Time{}, fmt.Errorf("--until must be an RFC3339 time or a duration like 2h or 3d, got %q", s)
}

// Park defers bead id. until zero parks it until Unpark. Parking an already
// parked bead only updates its time (idempotent); a closed bead is refused.
func Park(ctx context.Context, c Client, dir, id, who, reason string, until time.Time) error {
	b, err := c.Show(ctx, dir, id)
	if err != nil {
		return err
	}
	if b == nil {
		return fmt.Errorf("bead %s not found", id)
	}
	if b.Status == "closed" || b.Status == "tombstone" {
		return fmt.Errorf("%s is %s: only an open ask can be parked", id, b.Status)
	}
	w := NewStateWrite(c)
	changed := b.Status != StatusDeferred
	// A parked-from label on a bead that is not deferred is stale (an Unpark
	// that died half way): replace it with the status the bead has now.
	hasFrom := false
	for _, l := range b.Labels {
		if !strings.HasPrefix(l, ParkedFromPrefix) {
			continue
		}
		if b.Status == StatusDeferred {
			hasFrom = true
		} else if err := w.SetLabel(ctx, dir, id, l, false); err != nil {
			return err
		}
	}
	if !hasFrom {
		from := b.Status
		if from == StatusDeferred || from == "" {
			from = "open"
		}
		if err := w.SetLabel(ctx, dir, id, ParkedFromPrefix+from, true); err != nil {
			return err
		}
	}
	if err := w.SetLabel(ctx, dir, id, ParkedLabel, true); err != nil {
		return err
	}
	want := ""
	if !until.IsZero() {
		want = ParkedUntilPrefix + strconv.FormatInt(until.Unix(), 10)
	}
	for _, l := range b.Labels {
		if strings.HasPrefix(l, ParkedUntilPrefix) && l != want {
			changed = true
			if err := w.SetLabel(ctx, dir, id, l, false); err != nil {
				return err
			}
		}
	}
	if want != "" && !slices.Contains(b.Labels, want) {
		changed = true
		if err := w.SetLabel(ctx, dir, id, want, true); err != nil {
			return err
		}
	}
	if changed {
		// A fresh park or a new time is a new due date to announce.
		for _, l := range b.Labels {
			if strings.HasPrefix(l, ParkedNotifiedPrefix) {
				if err := w.SetLabel(ctx, dir, id, l, false); err != nil {
					return err
				}
			}
		}
	}
	if b.Status != StatusDeferred {
		if err := w.SetStatus(ctx, dir, id, StatusDeferred); err != nil {
			return err
		}
	}
	if err := w.Flush(ctx); err != nil {
		return err
	}
	if !changed {
		return nil
	}
	text := "parked by " + who
	if !until.IsZero() {
		text += " until " + until.UTC().Format(time.RFC3339)
	}
	if reason != "" {
		text += ": " + reason
	}
	return c.AddComment(ctx, dir, id, text)
}

// unparkStatuses are the statuses an unparked bead may return to.
var unparkStatuses = map[string]bool{"open": true, "in_progress": true, "blocked": true}

// Unpark restores the status the bead had before Park and drops the parking
// labels. A bead that is not deferred is left alone (idempotent). It reports
// whether it changed anything.
func Unpark(ctx context.Context, c Client, dir, id, who string) (bool, error) {
	b, err := c.Show(ctx, dir, id)
	if err != nil {
		return false, err
	}
	if b == nil {
		return false, fmt.Errorf("bead %s not found", id)
	}
	if b.Status != StatusDeferred {
		return false, nil
	}
	status := "open"
	for _, l := range b.Labels {
		// Older builds parsed `br show`'s header whole and stored "P2 · OPEN":
		// read the status out of it, as the header parser does now.
		if v, ok := strings.CutPrefix(l, ParkedFromPrefix); ok && unparkStatuses[normalizeStatus(v)] {
			status = normalizeStatus(v)
		}
	}
	// One update carries the status and every label removal, so a failure
	// cannot leave stale parking labels on an open bead.
	w := NewStateWrite(c)
	if err := w.SetStatus(ctx, dir, id, status); err != nil {
		return false, err
	}
	for _, l := range b.Labels {
		if l == ParkedLabel || strings.HasPrefix(l, ParkedFromPrefix) || strings.HasPrefix(l, ParkedUntilPrefix) || strings.HasPrefix(l, ParkedNotifiedPrefix) {
			if err := w.SetLabel(ctx, dir, id, l, false); err != nil {
				return false, err
			}
		}
	}
	if err := w.Flush(ctx); err != nil {
		return false, err
	}
	return true, c.AddComment(ctx, dir, id, "unparked by "+who)
}
