package beads

import "testing"

func TestParseLane(t *testing.T) {
	for v, want := range map[string]string{
		"you":                  "you|",
		"agents":               "agents|",
		"elsewhere":            "elsewhere|",
		"elsewhere:infra team": "elsewhere|infra team",
		LaneLabel("elsewhere", "the infra team")[len(LaneLabelPrefix):]: "elsewhere|the infra team",
	} {
		lane, who, err := ParseLane(v)
		if err != nil || lane+"|"+who != want {
			t.Errorf("ParseLane(%q) = %q %q %v, want %s", v, lane, who, err, want)
		}
	}
	for _, bad := range []string{"", "me", "you:someone", "agents:x", "operators"} {
		if _, _, err := ParseLane(bad); err == nil {
			t.Errorf("ParseLane(%q) accepted", bad)
		}
	}
	if got := LaneOf([]string{"kind:objective", "lane:elsewhere:legal"}); got != "elsewhere:legal" {
		t.Fatalf("LaneOf = %q", got)
	}
}
