package managerguard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// BeadsWriteWhy starts the Why of a refused direct bd/br write.
const BeadsWriteWhy = "writes beads directly: "

// AgentWriteGuardOff reports the kill switch: COMMANDER_AGENT_WRITE_GUARD=off
// (also 0, false, no) in the hook's environment stops the guards refusing
// direct bd/br writes.
func AgentWriteGuardOff() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("COMMANDER_AGENT_WRITE_GUARD"))) {
	case "off", "0", "false", "no":
		return true
	}
	return false
}

// bdWriteHints names, per write verb, the cmdr verb an agent runs instead.
var bdWriteHints = map[string]string{
	"create":    "cmdr internal-create-objective --project P --parent <id> --title T [--deps blocked-by:ID|blocks:ID] (operational sub-task: add --operational)",
	"q":         "cmdr internal-create-objective --project P --parent <id> --title T",
	"close":     "cmdr internal-close-objective --project P --bead <id> [--reason R]",
	"reopen":    "cmdr internal-set-objective --project P --bead <id> --status open",
	"set-state": "cmdr internal-phase --project P --bead <id> --phase <brainstorming|planning|executing|reviewing|done|...> [--reason R] (the cmdr phase), or cmdr internal-set-objective --state for the step state",
	"update":    "cmdr internal-update --project P --bead <id> (--title, --description, --notes, --acceptance, --priority), cmdr internal-set-objective --project P --bead <id> (--state, --status, --owner, --viz, --waiting, --review, --track, --hold ...), or cmdr internal-comment for a note",
	"edit":      "cmdr internal-update --project P --bead <id> (--title, --description, --notes, --acceptance, --priority)",
	"rename":    "cmdr internal-rename-objective --project P --bead <id> --title T",
	"comments":  "cmdr internal-comment --project P --bead <id> --text <note>",
	"dep":       "cmdr internal-dep add|rm --project P <issue> <depends-on> (create gates with cmdr internal-create-objective --gate --after/--before)",
	"label":     "cmdr internal-label --project P --bead <id> add|rm <label> (state labels have their own flags on cmdr internal-set-objective: --viz, --workstream, --hold, --waiting, --lane ...)",
	"defer":     "cmdr internal-set-objective --project P --bead <id> --park [--until T] [--reason R]",
	"undefer":   "cmdr internal-set-objective --project P --bead <id> --unpark",
	"assign":    "cmdr internal-set-objective --project P --bead <id> --owner <role>",
	"move":      "cmdr internal-place --ordinary --project P --bead <id> --parent <workstream> --dry-run (ordinary steps/cards, same owner), or cmdr internal-track convert --project P --bead <id> --into <workstream> (explicit workstream conversion)",
	"reparent":  "cmdr internal-place --ordinary --project P --bead <id> --parent <workstream> --dry-run (ordinary steps/cards, same owner), or cmdr internal-track convert --project P --bead <id> --into <workstream> (explicit workstream conversion)",
	"promote":   "cmdr internal-create-objective --project P --title T (a new workstream), or cmdr internal-track convert",
	"delete":    "cmdr internal-close-objective --project P --bead <id> --reason superseded (agents do not delete beads)",
	"merge":     "cmdr internal-mail --to $COMMANDER_MANAGER_ROLE (agents do not merge beads: ask)",
	"import":    "cmdr internal-mail --to $COMMANDER_MANAGER_ROLE (agents do not import beads: ask)",
	"init":      "cmdr internal-mail --to $COMMANDER_MANAGER_ROLE (agents do not initialise beads: ask)",
	"config":    "cmdr internal-mail --to $COMMANDER_MANAGER_ROLE (agents do not change beads config: ask)",
	BypassVerb:  "nothing: COMMANDER_BR_HELD and COMMANDER_AGENT_WRITE_GUARD are not for agents. Write beads with the cmdr verbs (cmdr internal-set-objective, internal-comment, internal-dep, internal-label, internal-update)",
	"sync":      "cmdr internal-mail --to $COMMANDER_MANAGER_ROLE (agents do not sync beads: ask)",
}

// RefusedVerbs lists every bd/br verb the guard refuses (for tests: each must
// have a hint naming a cmdr verb).
func RefusedVerbs() []string {
	out := []string{"sync", "comments", "dep", "label", "config"}
	for v := range bdWriteSubs {
		out = append(out, v)
	}
	return out
}

// BeadsWriteReason is what an agent sees for a direct bd/br write.
func BeadsWriteReason(verb string) string {
	hint := bdWriteHints[verb]
	if hint == "" {
		hint = "the matching cmdr verb (cmdr internal-set-objective, internal-create-objective, internal-close-objective, internal-comment, internal-dep, internal-gate)"
	}
	return "Refused: agents write beads only through the cmdr verbs, which go through cmdrd's write door and its checks, not with bd or br directly (`bd " + verb + "`). Use: " + hint + ". Reads (bd show, list, comments <id>, dep list, label list) stay allowed."
}

// BeadsWriteHookOutput refuses a direct bd/br write naming the cmdr verb.
func BeadsWriteHookOutput(verb string) []byte { return hookOutput(BeadsWriteReason(verb)) }

// BeadsWriteVerb is the verb of the first direct bd/br write in command, or ""
// (including under bash -c and env/timeout wrappers). A speed bump like
// TmuxKill: a script file or another tool gets around it. The kill switch is
// checked by the callers, not here.
func BeadsWriteVerb(command string) string {
	return beadsWriteVerb(command, 0)
}

func beadsWriteVerb(command string, depth int) string {
	if depth > 4 {
		return ""
	}
	for _, seg := range parse(command) {
		words := unwrap(stripGroup(seg.words), false)
		if len(words) > 0 {
			if baseName(words[0]) == "xargs" {
				words = unwrap(stripGroup(xargsCommand(words[1:])), false)
			}
		}
		if len(words) > 0 {
			switch baseName(words[0]) {
			case "bd", "br":
				if v := writeVerbOf(words[1:]); v != "" {
					return v
				}
			case "eval":
				// eval joins its arguments and runs them as a command line.
				if v := beadsWriteVerb(strings.Join(words[1:], " "), depth+1); v != "" {
					return v
				}
			case "bash", "sh", "zsh", "dash":
				for i, a := range words[1:] {
					if strings.HasPrefix(a, "-") && strings.Contains(a, "c") && i+2 < len(words) {
						if v := beadsWriteVerb(words[i+2], depth+1); v != "" {
							return v
						}
					}
				}
			}
		}
		for _, inner := range seg.subs {
			if v := beadsWriteVerb(inner, depth+1); v != "" {
				return v
			}
		}
	}
	return ""
}

// stripGroup drops the subshell/brace-group openers glued to the first word
// ("(bd", "{ bd"): the group's command is still a command.
func stripGroup(words []string) []string {
	for len(words) > 0 {
		w := strings.TrimLeft(words[0], "({")
		if w == words[0] {
			return words
		}
		if w != "" {
			return append([]string{w}, words[1:]...)
		}
		words = words[1:]
	}
	return words
}

// xargsValueFlags are xargs options that take the next word.
var xargsValueFlags = map[string]bool{"-I": true, "-n": true, "-P": true, "-L": true, "-d": true, "-E": true, "-s": true, "-a": true, "--max-args": true, "--max-procs": true, "--delimiter": true}

// xargsCommand is the command xargs runs: its words after its own options.
func xargsCommand(args []string) []string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return args[i+1:]
		}
		if !strings.HasPrefix(a, "-") {
			return args[i:]
		}
		if xargsValueFlags[a] {
			i++
		}
	}
	return nil
}

// NamesGuardBypass reports whether command names an environment variable that
// switches the agent write guard off (COMMANDER_BR_HELD, COMMANDER_AGENT_WRITE_GUARD),
// anywhere, not only next to a write verb: no agent has a reason to set them.
func NamesGuardBypass(command string) bool {
	return strings.Contains(command, "COMMANDER_BR_HELD") || strings.Contains(command, "COMMANDER_AGENT_WRITE_GUARD")
}

// BypassVerb is the pseudo-verb a bypass attempt is logged and refused under.
const BypassVerb = "bypass"

// bdValueFlags are global flags that take the next word.
var bdValueFlags = map[string]bool{"-C": true, "--db": true, "--directory": true, "--actor": true, "--lock-timeout": true, "--profile": true}

var bdWriteSubs = map[string]bool{
	"create": true, "q": true, "update": true, "close": true, "reopen": true, "delete": true,
	"defer": true, "undefer": true, "set-state": true, "import": true, "edit": true, "rename": true,
	"move": true, "promote": true, "merge": true, "init": true, "assign": true, "reparent": true,
}

// onlyHelp: the args after the subcommand are flags and ask for help
// ("bd update --help"); help writes nothing. Any non-flag word (a bead id, a
// value) means it is a real call.
func onlyHelp(rest []string) bool {
	help := false
	for _, r := range rest {
		if !strings.HasPrefix(r, "-") {
			return false
		}
		if r == "--help" || r == "-h" {
			help = true
		}
	}
	return help
}

// bdReadSubs are the bd/br verbs an agent may run directly: they read. Anything else is refused,
// including verbs that did not exist when this list was written (bd batch, sql, comment, note,
// tag, priority, link, gate, supersede, duplicate ...): an allow-list cannot miss a new write verb
// the way the old deny-list did.
var bdReadSubs = map[string]bool{
	"show": true, "list": true, "ready": true, "blocked": true, "children": true, "search": true, "query": true,
	"count": true, "status": true, "statuses": true, "types": true, "stale": true, "history": true, "lint": true,
	"graph": true, "export": true, "state": true, "info": true, "where": true, "context": true, "version": true,
	"help": true, "prime": true, "quickstart": true, "onboard": true, "memories": true, "recall": true,
	"find-duplicates": true, "stats": true, "diff": true,
}

// bdReadSecond are the sub-verbs of a verb that has both reads and writes.
var bdReadSecond = map[string]map[string]bool{
	"dep":    {"list": true, "tree": true, "cycles": true},
	"label":  {"list": true, "list-all": true},
	"config": {"get": true, "list": true},
	// bd human respond|dismiss close beads (m4); only its listing reads.
	"human": {"list": true, "show": true},
}

// writeVerbOf is the first non-read verb of a bd/br argument list (after the program), or "":
// the verb that is not on the read allow-list. A bare `bd` or one with only flags is no verb.
func writeVerbOf(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			sub, rest := a, args[i+1:]
			if onlyHelp(rest) {
				return ""
			}
			switch {
			case bdReadSubs[sub]:
				return ""
			case sub == "sync":
				for _, r := range rest {
					if r == "--flush-only" {
						return ""
					}
				}
				return sub
			case sub == "comments":
				// `comments <id>` and `comments list <id>` read; add/delete/remove/edit/update write.
				for _, r := range rest {
					switch r {
					case "add", "delete", "remove", "edit", "update":
						return sub
					}
				}
				return ""
			case bdReadSecond[sub] != nil:
				if len(rest) > 0 && bdReadSecond[sub][rest[0]] {
					return ""
				}
				return sub
			}
			return sub
		}
		if bdValueFlags[a] {
			i++
		}
		// A help or version flag before the verb is only that when no verb follows (`bd --version`):
		// `bd -v close X` (-v is --verbose) and `bd --help close X` still name a verb, judged below (M6).
	}
	return ""
}

// LogAgentWrite appends one refusal to <home>/logs/agent-write-refusals.jsonl.
func LogAgentWrite(home, role, command, verb string, now time.Time) error {
	line, err := json.Marshal(map[string]any{"role": role, "command": command, "verb": verb, "time": now.UTC().Format(time.RFC3339)})
	if err != nil {
		return err
	}
	return appendLine(filepath.Join(home, "logs", "agent-write-refusals.jsonl"), line)
}
