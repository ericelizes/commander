package managerguard

import "strings"

// CockpitWhy starts the Why of a refused command that opens the cockpit.
const CockpitWhy = "opens the cockpit: "

// CockpitReason is what an agent sees for it. cmdr refuses it too: this says so
// before the command runs. A speed bump, as with the other guards: `go run` of
// the command or a script file gets past this one, not past cmdr's own check.
const CockpitReason = "Bare cmdr, its --project and --task targets, root, restart and reload open the user's cockpit, and an agent never does. For the usage text run cmdr --help."

// CockpitHookOutput refuses a command that opens the cockpit.
func CockpitHookOutput() []byte { return hookOutput(CockpitReason) }

// OpensCockpit returns the first command of line that would build or open the
// cockpit, empty when none would.
func OpensCockpit(line string) string { return opensCockpit(line, 0) }

func opensCockpit(line string, depth int) string {
	if depth > 4 {
		return ""
	}
	for _, seg := range parse(line) {
		words := unwrap(stripGroup(seg.words), false)
		if looksUpCommand(stripGroup(seg.words)) {
			words = nil
		}
		if len(words) > 0 && baseName(words[0]) == "xargs" {
			words = unwrap(stripGroup(xargsCommand(words[1:])), false)
		}
		if len(words) > 0 {
			switch strings.TrimRight(baseName(words[0]), ")") {
			case "cmdr", "commander":
				if cockpitArgs(dropFDs(words[1:])) {
					return strings.Join(words, " ")
				}
			case "eval":
				if why := opensCockpit(strings.Join(words[1:], " "), depth+1); why != "" {
					return why
				}
			case "bash", "sh", "zsh", "dash":
				for i, a := range words[1:] {
					if strings.HasPrefix(a, "-") && strings.Contains(a, "c") && i+2 < len(words) {
						if why := opensCockpit(words[i+2], depth+1); why != "" {
							return why
						}
					}
				}
			}
		}
		for _, inner := range seg.subs {
			if why := opensCockpit(inner, depth+1); why != "" {
				return why
			}
		}
	}
	return ""
}

// looksUpCommand says words are `command -v x` or `command -V x`, which print
// where x is and run nothing. unwrap drops `command` and its flags, so this asks first.
func looksUpCommand(words []string) bool {
	for len(words) > 0 && (shellKeywords[words[0]] || envAssign.MatchString(words[0])) {
		words = words[1:]
	}
	return len(words) > 1 && baseName(words[0]) == "command" &&
		strings.HasPrefix(words[1], "-") && strings.ContainsAny(words[1], "vV")
}

// dropFDs removes what parse leaves of a redirect's fd ("2" of "2>&1") and a
// closing subshell paren. cmdr takes no bare number, so nothing is lost.
func dropFDs(args []string) []string {
	var out []string
	for _, a := range args {
		a = strings.TrimRight(a, ")")
		if a != "" && strings.Trim(a, "0123456789") != "" {
			out = append(out, a)
		}
	}
	return out
}

// cockpitArgs mirrors cmdr's run and parsePublicTarget: no arguments, the
// --project and --task targets, and the verbs that launch the session.
func cockpitArgs(args []string) bool {
	if len(args) == 0 {
		return true
	}
	for _, a := range args {
		if a == "--help" || a == "-h" {
			return false
		}
	}
	switch args[0] {
	case "root", "projects", "restart", "reload":
		return true
	}
	if !strings.HasPrefix(args[0], "-") {
		return false
	}
	name, _, _ := strings.Cut(strings.TrimLeft(args[0], "-"), "=")
	switch name {
	case "p", "project", "t", "task":
		return true
	}
	return false
}
