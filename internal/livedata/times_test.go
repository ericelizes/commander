package livedata

import (
	"testing"
	"time"
)

func TestHumanTimesReadInTheReadersZone(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no zone data")
	}
	SetUserLocation(ny)
	defer SetUserLocation(nil)
	now := time.Date(2026, 11, 12, 20, 22, 0, 0, time.UTC) // 3:22 PM EST
	cases := map[string]string{
		"Paused until Nov 13 14:00 UTC":                             "Paused until tomorrow at 9 AM EST",
		"Resume Nov 13 14:00 UTC: watch (final check by 20:00 UTC)": "Resume tomorrow at 9 AM EST: watch (final check by today at 3 PM EST)",
		"granted at 2026-11-12T18:03:00Z":                           "granted at today at 1:03 PM EST",
		"due Nov 20 15:30 UTC":                                      "due Nov 20 at 10:30 AM EST",
		"Waiting on CI #3592":                                       "Waiting on CI #3592",
	}
	for in, want := range cases {
		if got := HumanTimes(in, now); got != want {
			t.Fatalf("%q -> %q, want %q", in, got, want)
		}
	}
}
