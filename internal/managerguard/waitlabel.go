package managerguard

import (
	"regexp"
	"strings"
)

// WaitLabelReason is what an agent sees for a raw br write of a wait label.
const WaitLabelReason = "Refused: a wait label (waiting:... or w:...) is written only by `cmdr internal-set-objective --waiting`, which checks it is a wait cmdrd can watch and clear. Use --waiting ci:#N, review:<login> (with --review <PR>), main-ci:<sha>, prod-tag:<sha>, operator:<running role> or until:<RFC3339>:<plain words>; when it is the user's call, --state needs-user with --decision. Removing a wait label is fine."

// WaitLabelHookOutput refuses a raw wait-label write.
func WaitLabelHookOutput() []byte { return hookOutput(WaitLabelReason) }

var compactWaitLabel = regexp.MustCompile(`^w:[0-9a-z]+:(ci|mc|pt|rv|us|op|un)(:|$)`)

func isWaitLabel(l string) bool {
	return strings.HasPrefix(l, "waiting:") || compactWaitLabel.MatchString(l)
}

// WritesWaitLabel reports the br/bd command in command that adds a wait label
// by hand (label add, --add-label, create -l), or "". Hand-written waits skip
// the door's checks (internal-set-objective) and are how slugs nothing can
// clear got onto the board. A speed bump like TmuxKill: a script file gets around it.
func WritesWaitLabel(command string) string {
	return writesWaitLabel(command, 0)
}

func writesWaitLabel(command string, depth int) string {
	if depth > 4 {
		return ""
	}
	for _, words := range Commands(command) {
		for i, w := range words {
			switch baseName(w) {
			case "br", "bd":
				if brAddsWaitLabel(words[i+1:]) {
					return strings.Join(words, " ")
				}
			case "bash", "sh", "zsh", "dash":
				rest := words[i+1:]
				for j, a := range rest {
					if strings.HasPrefix(a, "-") && strings.Contains(a, "c") && j+1 < len(rest) {
						if why := writesWaitLabel(rest[j+1], depth+1); why != "" {
							return why
						}
					}
				}
			}
		}
	}
	return ""
}

func brAddsWaitLabel(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "label" && i+1 < len(args) && args[i+1] != "add" {
			return false // label remove / list
		}
		if a == "--remove-label" || a == "--remove-labels" {
			i++
			continue
		}
		if _, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(a, "-") {
			a = v
		}
		for _, l := range strings.Split(a, ",") {
			if isWaitLabel(strings.TrimSpace(l)) {
				return true
			}
		}
	}
	return false
}
