package livedata

import (
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var userLoc atomic.Pointer[time.Location]

// SetUserLocation is the zone the reader sees times in ([user] timezone).
func SetUserLocation(loc *time.Location) { userLoc.Store(loc) }

func userLocation() *time.Location {
	if l := userLoc.Load(); l != nil {
		return l
	}
	return time.Local
}

var (
	months   = map[string]time.Month{"Jan": 1, "Feb": 2, "Mar": 3, "Apr": 4, "May": 5, "Jun": 6, "Jul": 7, "Aug": 8, "Sep": 9, "Oct": 10, "Nov": 11, "Dec": 12}
	isoUTC   = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})[T ](\d{2}):(\d{2})(?::\d{2}(?:\.\d+)?)?(?:Z| ?UTC)`)
	monthUTC = regexp.MustCompile(`\b(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]* (\d{1,2})(?:,? (\d{4}))?(?:,? at)? (\d{1,2}):(\d{2}) ?UTC\b`)
	clockUTC = regexp.MustCompile(`\b(\d{1,2}):(\d{2}) ?UTC\b`)
	atoi     = func(s string) int { n, _ := strconv.Atoi(s); return n }
)

// HumanTimes rewrites the UTC times in text into the reader's zone, said
// relative to now: "Nov 13 14:00 UTC" reads "tomorrow at 9 AM EST".
func HumanTimes(text string, now time.Time) string {
	if !strings.Contains(text, "UTC") && !strings.Contains(text, "Z") {
		return text
	}
	loc := userLocation()
	text = isoUTC.ReplaceAllStringFunc(text, func(m string) string {
		p := isoUTC.FindStringSubmatch(m)
		t := time.Date(atoi(p[1]), time.Month(atoi(p[2])), atoi(p[3]), atoi(p[4]), atoi(p[5]), 0, 0, time.UTC)
		return humanAt(t, now, loc)
	})
	text = monthUTC.ReplaceAllStringFunc(text, func(m string) string {
		p := monthUTC.FindStringSubmatch(m)
		year := now.UTC().Year()
		if p[3] != "" {
			year = atoi(p[3])
		}
		t := time.Date(year, months[p[1]], atoi(p[2]), atoi(p[4]), atoi(p[5]), 0, 0, time.UTC)
		if p[3] == "" { // no year written: the one closest to now
			if t.Sub(now) > 183*24*time.Hour {
				t = t.AddDate(-1, 0, 0)
			} else if now.Sub(t) > 183*24*time.Hour {
				t = t.AddDate(1, 0, 0)
			}
		}
		return humanAt(t, now, loc)
	})
	text = clockUTC.ReplaceAllStringFunc(text, func(m string) string {
		p := clockUTC.FindStringSubmatch(m)
		u := now.UTC()
		return humanAt(time.Date(u.Year(), u.Month(), u.Day(), atoi(p[1]), atoi(p[2]), 0, 0, time.UTC), now, loc)
	})
	return text
}

// humanAt is t in loc, said from now: "today at 3 PM EST", "tomorrow at 9:30
// AM EST", "Mon at 9 AM EST", "Nov 20 at 9 AM EST".
func humanAt(t, now time.Time, loc *time.Location) string {
	lt, ln := t.In(loc), now.In(loc)
	day := func(x time.Time) time.Time { return time.Date(x.Year(), x.Month(), x.Day(), 0, 0, 0, 0, time.UTC) }
	days := int(day(lt).Sub(day(ln)).Hours() / 24)
	clock := lt.Format("3:04 PM")
	if lt.Minute() == 0 {
		clock = lt.Format("3 PM")
	}
	clock += " " + lt.Format("MST")
	switch {
	case days == 0:
		return "today at " + clock
	case days == 1:
		return "tomorrow at " + clock
	case days == -1:
		return "yesterday at " + clock
	case days > 1 && days < 7:
		return lt.Format("Mon") + " at " + clock
	}
	return lt.Format("Jan 2") + " at " + clock
}
