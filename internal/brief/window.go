// Package brief writes the Brief page: what the user is moving today, what
// they finished on the previous workday and how this week's workstreams moved,
// in the first person, every sentence cited. cmdrd rewrites it at 7 AM Eastern
// and every 6 hours after, and on demand; the latest is kept in brief.json.
package brief

import "time"

// Zone is where the Brief's days and schedule are counted.
var Zone = loadZone()

func loadZone() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.Local
	}
	return loc
}

// slotHours are the hours the Brief is rewritten: 7 AM, then every 6 hours.
var slotHours = []int{1, 7, 13, 19}

// NextSlot is the first scheduled rewrite strictly after t.
func NextSlot(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	for d := 0; ; d++ {
		for _, h := range slotHours {
			s := time.Date(t.Year(), t.Month(), t.Day()+d, h, 0, 0, 0, loc)
			if s.After(t) {
				return s
			}
		}
	}
}

// Window is a span of time, [Start, End).
type Window struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Label string    `json:"label"`
}

// Windows are the Brief's three spans at one moment.
type Windows struct {
	Now       time.Time `json:"now"`
	Today     Window    `json:"today"`
	Yesterday Window    `json:"yesterday"` // the last 24 hours, through now
	Week      Window    `json:"week"`      // Monday 00:00 through now
}

// WindowsAt are the windows at now, with days counted in loc.
func WindowsAt(now time.Time, loc *time.Location) Windows {
	n := now.In(loc)
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	day := n.Add(-24 * time.Hour)
	monday := time.Date(n.Year(), n.Month(), n.Day()-(int(n.Weekday())+6)%7, 0, 0, 0, 0, loc)
	return Windows{
		Now:       n,
		Today:     Window{Start: today, End: n, Label: today.Format("Monday, Jan 2")},
		Yesterday: Window{Start: day, End: n, Label: "since " + day.Format("Mon 3:04 PM")},
		Week:      Window{Start: monday, End: n, Label: monday.Format("Mon Jan 2") + " to now"},
	}
}

// Since is the earliest time any window reaches back to.
func (w Windows) Since() time.Time {
	if w.Yesterday.Start.Before(w.Week.Start) {
		return w.Yesterday.Start
	}
	return w.Week.Start
}

// When names the window t falls in: today, yesterday, week, or "" (none).
func (w Windows) When(t time.Time) string {
	switch {
	case t.IsZero() || t.After(w.Now):
		return ""
	case !t.Before(w.Today.Start):
		return "today"
	case !t.Before(w.Yesterday.Start):
		return "yesterday"
	case !t.Before(w.Week.Start):
		return "week"
	}
	return ""
}

// sameDay reports whether t falls on now's calendar day in loc.
func sameDay(t, now time.Time, loc *time.Location) bool {
	a, b := t.In(loc), now.In(loc)
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}
