package beads

import (
	"fmt"
	"strings"
)

// A lane is whose move a step is in the derived diagram: the user's, the
// agents', or someone elsewhere (a reviewer, CI, a clock, another team). The
// diagram derives it from owner, waits, gates and needs-user; the lane label
// says it only where none of those can, e.g. a step another team will do:
//
//	lane:you | lane:agents | lane:elsewhere | lane:elsewhere:<who, EscapeLabel'd>
const LaneLabelPrefix = "lane:"

// The lanes.
const (
	LaneYou       = "you"
	LaneAgents    = "agents"
	LaneElsewhere = "elsewhere"
)

// ParseLane reads a lane value ("elsewhere:the infra team", or a label's
// escaped form): the lane and, for elsewhere, who it is with.
func ParseLane(v string) (lane, who string, err error) {
	lane, who, _ = strings.Cut(strings.TrimSpace(v), ":")
	who = strings.TrimSpace(UnescapeLabel(who))
	switch lane {
	case LaneYou, LaneAgents:
		if who != "" {
			return "", "", fmt.Errorf("lane %q: only elsewhere names who it is with (elsewhere:<who>)", v)
		}
	case LaneElsewhere:
	default:
		return "", "", fmt.Errorf("lane %q: want you, agents, elsewhere or elsewhere:<who>", v)
	}
	return lane, who, nil
}

// LaneLabel is the label for a lane value ParseLane accepts.
func LaneLabel(lane, who string) string {
	if who == "" {
		return LaneLabelPrefix + lane
	}
	return LaneLabelPrefix + lane + ":" + EscapeLabel(who)
}

// LaneOf is the raw value of a bead's lane label ("" = none).
func LaneOf(labels []string) string {
	for _, l := range labels {
		if v, ok := strings.CutPrefix(l, LaneLabelPrefix); ok {
			return v
		}
	}
	return ""
}
