// Package operatorguard keeps full builds and test suites inside the
// machine's build slots: a classifier for the Bash commands an Operator
// runs, the Claude PreToolUse hook output, and the refusal log.
package operatorguard

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/example-user/commander/internal/managerguard"
)

// IsOperatorRole reports whether role is an Operator.
func IsOperatorRole(role string) bool { return strings.HasPrefix(role, "operator-") }

// Verdict is the classifier's answer for one Bash command.
type Verdict struct {
	Deny bool
	Why  string // the offending command; empty when allowed
	Scan bool   // refused as a root-wide filesystem scan, not for skipping the slot
	// Tmux is set when refused for killing the tmux server or another session.
	Tmux bool
	// Update is set when refused for touching the app's install endpoints.
	Update bool
	// Wait is set when refused for writing a wait label by hand.
	Wait bool
	// Beads is set when refused for writing beads with bd/br directly; Verb is the bd verb.
	Beads      bool
	Verb       string
	Credential bool
	// Cockpit is set when refused for opening the cockpit (bare cmdr and its targets).
	Cockpit bool
	// Stash is set when refused for git stash: every worktree shares one stash list (refs/stash),
	// so a push, pop, apply or drop can take or lose another task's work.
	Stash bool
}

// StashReason tells an Operator how to set work aside without the shared stash.
const StashReason = "Refused: git stash. Every worktree of the repo shares one stash list (refs/stash), so a stash, pop, apply or drop can take or lose another Operator's work. " +
	"Set work aside with a WIP commit on your own branch instead (git add -A && git commit -m 'wip: ...'; undo it later with git reset --soft HEAD~1). git stash list and git stash show stay allowed."

// DenyReason tells a refused Operator how to proceed.
func DenyReason(why string) string {
	return "Full builds and suites share the machine's build slots. Refused: " + why + ". " +
		"Wrap it: cmdr internal-slot run -- <the same command>. While iterating, run targeted tests only " +
		"(go test -p 2 ./pkg -run X, vitest <file>, pytest path/to/test_x.py::name)."
}

// ScanDenyReason tells an Operator that a whole-filesystem search is refused.
func ScanDenyReason(why string) string {
	return "Refused: " + why + ". A search from /, /Users or the home directory walks the whole machine " +
		"and starves every other Operator. Search inside the repo or a named directory instead, " +
		"or use git grep / rg in the worktree."
}

// HookOutput is the Claude Code PreToolUse JSON that refuses a tool call.
func HookOutput(why string) []byte { return hookOutput(DenyReason(why)) }

// HookOutputFor is HookOutput with the reason that fits the verdict.
func HookOutputFor(v Verdict) []byte {
	if v.Credential {
		return hookOutput("Credential files and cmdr linear set-key belong to the user; agents may use cmdr linear status.")
	}
	if v.Update {
		return hookOutput(managerguard.UpdateAPIDenyReason)
	}
	if v.Tmux {
		return hookOutput(managerguard.TmuxKillReason)
	}
	if v.Beads {
		return hookOutput(managerguard.BeadsWriteReason(v.Verb))
	}
	if v.Wait {
		return hookOutput(managerguard.WaitLabelReason)
	}
	if v.Cockpit {
		return hookOutput(managerguard.CockpitReason)
	}
	if v.Scan {
		return hookOutput(ScanDenyReason(v.Why))
	}
	if v.Stash {
		return hookOutput(StashReason)
	}
	return HookOutput(v.Why)
}

func hookOutput(reason string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":            "PreToolUse",
		"permissionDecision":       "deny",
		"permissionDecisionReason": reason,
	}})
	return bytes.TrimSpace(buf.Bytes())
}

// LogRefusal appends one refusal to <home>/logs/operator-refusals.jsonl.
func LogRefusal(home, role, command, why string, now time.Time) error {
	line, err := json.Marshal(map[string]any{"role": role, "command": command, "why": why, "time": now.UTC().Format(time.RFC3339)})
	if err != nil {
		return err
	}
	return appendLine(filepath.Join(home, "logs", "operator-refusals.jsonl"), line)
}

// LogError records a hook failure (the hook itself fails open).
func LogError(home, role, msg string, now time.Time) {
	line, _ := json.Marshal(map[string]any{"role": role, "error": msg, "time": now.UTC().Format(time.RFC3339)})
	_ = appendLine(filepath.Join(home, "logs", "operator-guard-errors.jsonl"), line)
}

func appendLine(path string, line []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// Classify decides whether an Operator may run command as is. A command that
// already starts with `cmdr internal-slot run` (or `lockf`) is the wrapped form.
func Classify(command string) Verdict {
	if managerguard.NamesProtectedCredential(command) {
		return Verdict{Deny: true, Why: "names a protected credential file", Credential: true}
	}
	if managerguard.TouchesUpdateAPI(command) {
		return Verdict{Deny: true, Why: "touches the install endpoints: /api/update", Update: true}
	}
	if why := managerguard.TmuxKill(command); why != "" {
		return Verdict{Deny: true, Why: "kills tmux: " + why, Tmux: true}
	}
	if why := managerguard.WritesWaitLabel(command); why != "" {
		return Verdict{Deny: true, Why: managerguard.WaitLabelWhy + why, Wait: true}
	}
	if why := managerguard.OpensCockpit(command); why != "" {
		return Verdict{Deny: true, Why: managerguard.CockpitWhy + why, Cockpit: true}
	}
	if !managerguard.AgentWriteGuardOff() {
		if managerguard.NamesGuardBypass(command) {
			return Verdict{Deny: true, Why: managerguard.BeadsWriteWhy + managerguard.BypassVerb, Beads: true, Verb: managerguard.BypassVerb}
		}
		if verb := managerguard.BeadsWriteVerb(command); verb != "" {
			return Verdict{Deny: true, Why: managerguard.BeadsWriteWhy + verb, Beads: true, Verb: verb}
		}
	}
	return classify(command, 0)
}

var envAssign = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

func classify(command string, depth int) Verdict {
	if depth > 4 {
		return Verdict{}
	}
	for _, words := range managerguard.Commands(command) {
		if v := classifyWords(words, depth); v.Deny {
			return v
		}
	}
	return Verdict{}
}

func classifyWords(words []string, depth int) Verdict {
	for len(words) > 0 {
		w := words[0]
		switch {
		case envAssign.MatchString(w):
			words = words[1:]
		case base(w) == "env" || base(w) == "command" || base(w) == "time" || base(w) == "nohup" || base(w) == "nice":
			words = words[1:]
			for len(words) > 0 && (strings.HasPrefix(words[0], "-") || envAssign.MatchString(words[0])) {
				if words[0] == "-u" || words[0] == "-C" || words[0] == "-S" { // takes a value
					words = words[1:]
				}
				if len(words) > 0 {
					words = words[1:]
				}
			}
		case base(w) == "timeout" && len(words) > 1:
			words = words[2:]
		default:
			goto ready
		}
	}
ready:
	if len(words) == 0 {
		return Verdict{}
	}
	cmd, args := base(words[0]), words[1:]
	if managerguard.HelpRequest(words) {
		return Verdict{}
	}
	if strings.HasPrefix(cmd, "python3.") {
		cmd = "python3"
	}
	deny := func() Verdict { return Verdict{Deny: true, Why: strings.Join(words, " ")} }
	if isScanTool(cmd) && scansBroadly(cmd, args) {
		return Verdict{Deny: true, Why: strings.Join(words, " "), Scan: true}
	}
	switch cmd {
	case "git":
		if sub, rest := gitSubcommand(args); sub == "stash" && !(len(rest) > 0 && (rest[0] == "list" || rest[0] == "show")) {
			return Verdict{Deny: true, Why: strings.Join(words, " "), Stash: true}
		}
	case "cmdr", "commander":
		if len(args) >= 2 && args[0] == "linear" && args[1] == "set-key" {
			return Verdict{Deny: true, Why: "cmdr linear set-key belongs to the user", Credential: true}
		}
		return Verdict{}
	case "lockf", "flock":
		return Verdict{} // cmdr internal-slot run -- ..., lockf <file> ..., flock <file> ...
	case "bash", "sh", "zsh", "dash":
		for i, a := range args {
			if strings.HasPrefix(a, "-") && strings.Contains(a, "c") && i+1 < len(args) {
				return classify(args[i+1], depth+1)
			}
		}
	case "go":
		if len(args) > 0 && (args[0] == "test" || args[0] == "build" || args[0] == "vet") && hasPattern(args[1:]) {
			return deny()
		}
	case "vitest":
		if !hasFileArg(args, "vitest", "run", "watch") {
			return deny()
		}
	case "npx":
		for i, a := range args {
			if a == "vitest" && !hasFileArg(args[i+1:], "run", "watch") {
				return deny()
			}
		}
	case "npm", "pnpm", "yarn", "bun":
		return classifyNpm(args, deny)
	case "pytest", "py.test":
		if !hasPytestTarget(args) {
			return deny()
		}
	case "python", "python3":
		if len(args) > 1 && args[0] == "-m" && args[1] == "pytest" && !hasPytestTarget(args[2:]) {
			return deny()
		}
	case "uvx":
		return classifyWords(skipRunnerFlags(args), depth)
	case "uv", "poetry", "pipenv", "hatch", "pdm":
		if len(args) > 0 && args[0] == "run" {
			return classifyWords(skipRunnerFlags(args[1:]), depth)
		}
	case "just":
		if recipe, rest := justRecipe(args); recipe == "test" && !hasPytestTarget(rest) {
			return deny()
		}
	case "ansible-playbook", "molecule":
		return deny()
	case "cargo":
		if len(args) > 0 && (args[0] == "test" || args[0] == "build") {
			return deny()
		}
	case "make", "gmake":
		if len(args) > 0 && args[0] == "test" {
			return deny()
		}
	case "docker":
		if len(args) > 0 && args[0] == "build" {
			return deny()
		}
	}
	return Verdict{}
}

func isScanTool(cmd string) bool {
	switch cmd {
	case "find", "bfs", "fd", "fdfind", "mdfind", "locate", "rg", "ag", "grep", "egrep", "fgrep":
		return true
	}
	return false
}

// scansBroadly reports whether a search command walks from /, /Users, /home or
// the home directory. grep only walks when recursive; mdfind without a narrow
// -onlyin searches the whole machine. For grep/rg/ag the first positional is
// the pattern, not a path.
func scansBroadly(cmd string, args []string) bool {
	switch cmd {
	case "grep", "egrep", "fgrep":
		if !recursiveGrep(args) {
			return false
		}
	case "mdfind":
		for i, a := range args {
			if a == "-onlyin" && i+1 < len(args) && !broadPath(args[i+1]) {
				return false
			}
		}
		return true
	case "locate":
		return true
	}
	skipPattern := cmd == "grep" || cmd == "egrep" || cmd == "fgrep" || cmd == "rg" || cmd == "ag"
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if skipPattern {
			skipPattern = false
			continue
		}
		if broadPath(a) {
			return true
		}
	}
	return false
}

func recursiveGrep(args []string) bool {
	for _, a := range args {
		switch {
		case a == "--recursive" || a == "--dereference-recursive":
			return true
		case strings.HasPrefix(a, "--"):
		case strings.HasPrefix(a, "-") && strings.ContainsAny(a, "rR"):
			return true
		}
	}
	return false
}

var homeDirRe = regexp.MustCompile(`^/(Users|home)(/[^/]+)?$`)

// broadPath reports whether p is /, /Users, /home, a home directory, ~ or $HOME.
func broadPath(p string) bool {
	p = strings.TrimRight(p, "/")
	switch p {
	case "", "~", "$HOME", "${HOME}":
		return true // "" is / itself
	}
	if homeDirRe.MatchString(p) {
		return true
	}
	h, err := os.UserHomeDir()
	return err == nil && p == strings.TrimRight(h, "/")
}

// pytestValueFlags are pytest options whose value is a separate word, so it
// is not read as a test path.
var pytestValueFlags = map[string]bool{
	"-k": true, "-m": true, "-p": true, "-n": true, "-c": true, "-o": true, "-W": true,
	"--ignore": true, "--ignore-glob": true, "--deselect": true, "--rootdir": true, "--maxfail": true,
	"--durations": true, "--tb": true, "--cov": true, "--cov-report": true, "--junitxml": true,
	"--basetemp": true, "--confcutdir": true, "--log-level": true, "--dist": true, "--numprocesses": true,
}

// hasPytestTarget reports whether pytest args name a test file or node id
// (tests/test_x.py, tests/test_x.py::name). A bare directory still collects a
// whole suite, so it does not count.
func hasPytestTarget(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if pytestValueFlags[a] {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		if strings.Contains(a, "::") || strings.HasSuffix(a, ".py") {
			return true
		}
	}
	return false
}

// skipRunnerFlags drops `uv run`-style options, leaving the command they run.
func skipRunnerFlags(args []string) []string {
	takesValue := map[string]bool{
		"--with": true, "--python": true, "-p": true, "--project": true, "--directory": true,
		"--extra": true, "--group": true, "--env-file": true, "--package": true, "--with-requirements": true,
	}
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		if args[0] == "--" {
			return args[1:]
		}
		if takesValue[args[0]] && len(args) > 1 {
			args = args[1:]
		}
		args = args[1:]
	}
	return args
}

// justRecipe returns the recipe `just` runs and its arguments.
func justRecipe(args []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-f" || a == "--justfile" || a == "-d" || a == "--working-directory" || a == "--set":
			i++
			if a == "--set" {
				i++
			}
		case strings.HasPrefix(a, "-"), envAssign.MatchString(a):
		default:
			return a, args[i+1:]
		}
	}
	return "", nil
}

// classifyNpm refuses builds and un-targeted test runs.
func classifyNpm(args []string, deny func() Verdict) Verdict {
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--prefix" || a == "-C" || a == "--workspace" || a == "-w":
			i++ // takes a value
		case !strings.HasPrefix(a, "-") || a == "--":
			rest = append(rest, a)
		}
	}
	if len(rest) == 0 {
		return Verdict{}
	}
	script, after := rest[0], rest[1:]
	if script == "run" || script == "run-script" {
		if len(after) == 0 {
			return Verdict{}
		}
		script, after = after[0], after[1:]
	}
	switch {
	case script == "test" || script == "t" || script == "tst" || strings.HasPrefix(script, "test:"):
		if !hasFileArg(after, "--") {
			return deny()
		}
	case script == "build" || script == "dist" || script == "shots" || strings.HasPrefix(script, "smoke"):
		return deny()
	}
	return Verdict{}
}

// hasPattern reports whether go args name a "..." package pattern.
func hasPattern(args []string) bool {
	for _, a := range args {
		if strings.Contains(a, "...") {
			return true
		}
	}
	return false
}

// hasFileArg reports whether args hold a positional that narrows a test run
// (a file or name filter), ignoring flags and the listed subcommand words.
func hasFileArg(args []string, ignore ...string) bool {
	skip := map[string]bool{}
	for _, s := range ignore {
		skip[s] = true
	}
	for _, a := range args {
		if !strings.HasPrefix(a, "-") && !skip[a] {
			return true
		}
	}
	return false
}

func base(w string) string { return filepath.Base(w) }

// gitSubcommand skips git's global options (-C <dir>, -c <k=v>, --git-dir=..., -P ...) and
// returns the subcommand and its arguments.
// gitValueOpts are git's global options that take their value as the next word (m5): skipped with
// it, so `git --attr-source HEAD stash pop` still finds stash.
var gitValueOpts = map[string]bool{
	"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true,
	"--config-env": true, "--attr-source": true, "--super-prefix": true, "--exec-path": true, "--list-cmds": true,
}

func gitSubcommand(args []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case gitValueOpts[a]:
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a, args[i+1:]
		}
	}
	return "", nil
}
