package managerguard

import (
	"regexp"
	"strings"
)

// TmuxKillReason is what an agent sees for a command that would kill the tmux
// server or sessions that are not its own.
const TmuxKillReason = "Refused: this would kill a tmux server or a session that is not yours. The commander-ops server holds every Operator's session (incident 2026-10-26: one kill-server ended 10 Operators). Read verbs (ls, list-*, capture-pane) are fine; ending a session is the launcher's and the reaper's job."

// tmuxWord matches what a kill aimed at the tmux server names: tmux itself or
// the commander-ops server (pgrep -f commander-ops).
var tmuxWord = regexp.MustCompile(`(?i)\btmux\b|commander-ops`)

// tmuxFlagsWithValue are the tmux global options that take a separate value.
var tmuxFlagsWithValue = map[string]bool{"-L": true, "-S": true, "-f": true, "-c": true, "-T": true}

// TmuxKill reports why command kills the tmux server or sessions other than
// the caller's own, or "" when it does not. Refused: `tmux [-L x] kill-server`,
// `kill-session -a`, `kill-session -t <anything>` (the target cannot be proven
// to be the caller's; a bare `kill-session` ends the pane's own session),
// `killall|pkill tmux`, and `kill`/`xargs kill` in a line that names tmux
// (kill $(pgrep tmux), kill $(pgrep -f commander-ops), tmux display -p
// '#{pid}' | xargs kill). tmux accepts unique prefixes of command names
// (kill-serv) and sequences (ls \; kill-server), run-shell/if-shell/#( ) run
// more commands, and kill-window/kill-pane/respawn-pane -k/send-keys exit can
// end another session's last pane, so all of those are refused when they name
// a target (-t, -a) or another server (-L, -S). A bare kill-session,
// kill-window or kill-pane ends the caller's own. A speed bump
// like TouchesUpdateAPI: a script file gets around it.
func TmuxKill(command string) string {
	return tmuxKill(command, 0)
}

func tmuxKill(command string, depth int) string {
	if depth > 4 {
		return ""
	}
	namesTmux := tmuxWord.MatchString(command)
	if tmuxHiddenShell(command) {
		return command
	}
	ctx := tmuxCtx{whole: namesTmux && killWord.MatchString(command), written: tmuxWrites(command)}
	for _, words := range Commands(command) {
		if why := tmuxKillWords(words, namesTmux, ctx, depth); why != "" {
			return why
		}
	}
	return ""
}

// tmuxCtx is what a segment's check may know about the whole line: whole =
// the line names tmux and kill anywhere ($T kill-server, text piped to a
// shell); written = a segment redirects tmux-and-kill text into a file, so a
// shell running a file may be running it.
type tmuxCtx struct{ whole, written bool }

// tmuxWrites reports whether a segment redirects output (not to /dev/null)
// and its own words name tmux and kill (echo 'tmux kill-server' > x.sh).
func tmuxWrites(command string) bool {
	for _, seg := range parse(command) {
		text := strings.Join(seg.words, " ")
		if !tmuxWord.MatchString(text) || !killWord.MatchString(text) {
			continue
		}
		for _, r := range seg.redirects {
			if r != "/dev/null" {
				return true
			}
		}
	}
	return false
}

// tmuxSinkWord reports whether words[i] runs as a command: first word, or
// behind only wrappers, flags and assignments (env X=1 sh, sudo -u x bash,
// xargs sh). A sink name as an argument (git grep x -- docs/python) is data.
func tmuxSinkWord(words []string, i int) bool {
	wrapped, prevFlag := false, false
	for _, w := range words[:i] {
		w = strings.TrimLeft(w, "{(")
		switch {
		case w == "":
			// a bare { or ( group opener
		case strings.HasPrefix(w, "-"):
			prevFlag = wrapped
			continue
		case strings.Contains(w, "="):
		case tmuxWrappers[baseName(w)]:
			wrapped = true
		case wrapped && (prevFlag || isNumber(w)): // a wrapper's flag value or count
		default:
			return false
		}
		prevFlag = false
	}
	return true
}

func isNumber(w string) bool {
	if w == "" {
		return false
	}
	for _, r := range w {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

var tmuxWrappers = map[string]bool{
	"env": true, "sudo": true, "xargs": true, "time": true, "nohup": true, "exec": true,
	"command": true, "timeout": true, "nice": true, "setsid": true, "doas": true,
	"busybox": true, "stdbuf": true, "ionice": true,
}

// killWord is what a shell, interpreter or heredoc body must also contain, next
// to a tmux word, to be refused where its text cannot be parsed (a speed bump).
// It starts a word, so skill and overkill are not kills; whatever follows still
// counts (killall, killServer, kill_session), and so do pkill and SIGKILL. A
// backslash escape (\t, \n, \x20, \040,  ) starts a word too: the shell or
// interpreter turns it into a separator. Cost: a literal dir\skills reads as a kill.
var killWord = regexp.MustCompile(`(?i)(?:^|[^a-z0-9]|\\(?:x[0-9a-f]{1,2}|u[0-9a-f]{1,8}|[0-7]{1,4}|[a-z]))(?:p|sig)?kill`)

// tmuxSinks are programs that run text they are given: shells (stdin), script
// and interpreters (-c, -e).
var tmuxSinks = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "script": true, "python": true,
	"python3": true, "perl": true, "ruby": true, "node": true,
}

// tmuxDecoders turn hidden text back into a command line (base64 -d | sh).
var tmuxDecoders = map[string]bool{"base64": true, "xxd": true, "rev": true, "openssl": true}

// tmuxHiddenShell: text the word parser cannot see reaching a shell. Heredocs
// and here-strings naming tmux and kill, eval of a substitution naming both,
// and a decoder piped into a shell (the decoded text hides the tmux word).
func tmuxHiddenShell(command string) bool { return tmuxHiddenShellAt(command, 0) }

func tmuxHiddenShellAt(command string, depth int) bool {
	if depth > 4 {
		return false
	}
	fed := false
	for _, seg := range parse(command) {
		for _, sub := range seg.subs {
			if tmuxHiddenShellAt(sub, depth+1) {
				return true
			}
		}
		words := seg.words
		input := seg.inputText
		if seg.hereString {
			input += strings.Join(words, " ")
		}
		namedInput := tmuxWord.MatchString(input) && killWord.MatchString(input)
		text := strings.Join(words, " ")
		namedSub := len(seg.subs) > 0 && tmuxWord.MatchString(text) && killWord.MatchString(text)
		for i, w := range words {
			b := baseName(w)
			if !tmuxSinkWord(words, i) {
				continue
			}
			if namedInput && (tmuxSinks[b] || b == "eval") || namedSub && b == "eval" {
				return true
			}
			if tmuxDecoders[b] {
				fed = true
			}
			if fed && (b == "sh" || b == "bash" || b == "zsh" || b == "eval") {
				return true
			}
		}
		if !seg.piped {
			fed = false
		}
	}
	return false
}

func tmuxKillWords(words []string, namesTmux bool, ctx tmuxCtx, depth int) string {
	words = append([]string(nil), words...)
	for i, w := range words { // (tmux kill-server), { tmux kill-server; }
		words[i] = strings.TrimRight(strings.TrimLeft(w, "({"), ")}")
	}
	for i, w := range words {
		if !tmuxSinkWord(words, i) {
			continue
		}
		cmd := baseName(w)
		rest := words[i+1:]
		if i == 0 && strings.HasPrefix(w, "$") && ctx.whole { // $T kill-server, T=tmux
			return strings.Join(words, " ")
		}
		if tmuxSinks[cmd] && tmuxSinkWord(words, i) && tmuxSinkFed(rest, ctx) {
			// text fed to a shell/interpreter: stdin, -c, -e, script -c, a written file
			return strings.Join(words, " ")
		}
		switch cmd {
		case "echo", "printf":
			if i == 0 { // the rest is data, not a command (substitutions are scanned apart)
				return ""
			}
		case "eval":
			if why := tmuxKill(strings.Join(rest, " "), depth+1); why != "" {
				return why
			}
		case "tmux":
			if why := tmuxSubcommand(words, rest, depth); why != "" {
				return why
			}
		case "killall", "pkill":
			for _, a := range rest {
				if tmuxWord.MatchString(a) {
					return strings.Join(words, " ")
				}
			}
		case "kill":
			if namesTmux {
				return strings.Join(words, " ")
			}
		case "bash", "sh", "zsh", "dash":
			for j, a := range rest {
				if strings.HasPrefix(a, "-") && strings.Contains(a, "c") && j+1 < len(rest) {
					if why := tmuxKill(rest[j+1], depth+1); why != "" {
						return why
					}
				}
			}
		case "xargs":
			for _, a := range rest {
				if baseName(a) == "kill" && namesTmux {
					return strings.Join(words, " ")
				}
			}
			if why := tmuxKillWords(rest, namesTmux, ctx, depth+1); why != "" {
				return why
			}
		}
	}
	return ""
}

// tmuxDangerous are the tmux commands (canonical names) that can end a server,
// a session or a pane, or run something that can.
var tmuxDangerous = []string{
	"kill-server", "kill-session", "kill-window", "kill-pane", "run-shell", "if-shell",
	"source-file", "respawn-pane", "send-keys", "new-window", "new-session", "split-window",
	"pipe-pane", "set-hook", "bind-key", "confirm-before", "command-prompt", "display-menu",
	"respawn-window", "detach-client", "display-popup",
}

// tmuxAliases are tmux's own short names; "run" is the one the task names.
var tmuxAliases = map[string]string{
	"run": "run-shell", "if": "if-shell", "source": "source-file", "killp": "kill-pane",
	"killw": "kill-window", "respawnp": "respawn-pane", "send": "send-keys",
	"neww": "new-window", "new": "new-session", "splitw": "split-window", "pipep": "pipe-pane",
	"set-hook": "set-hook", "bind": "bind-key", "confirm": "confirm-before", "menu": "display-menu",
	"respawnw": "respawn-window", "detach": "detach-client", "popup": "display-popup",
}

// tmuxCandidates resolves a typed tmux command name to every dangerous command
// it could mean: an alias or exact name, else any unique-prefix reading (two
// letters or more; an ambiguous one is refused as its worst reading).
func tmuxCandidates(name string) []string {
	if full, ok := tmuxAliases[name]; ok {
		return []string{full}
	}
	var out []string
	for _, c := range tmuxDangerous {
		if c == name || len(name) >= 2 && strings.HasPrefix(c, name) {
			out = append(out, c)
		}
	}
	return out
}

func tmuxSubcommand(all, args []string, depth int) string {
	other := false // -L / -S: a server that may not be the caller's
	for i := 0; i < len(args); i++ {
		a := args[i]
		if tmuxFlagsWithValue[a] {
			other = other || a == "-L" || a == "-S"
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			other = other || strings.HasPrefix(a, "-L") || strings.HasPrefix(a, "-S")
			continue
		}
		if strings.HasPrefix(a, "$") { // tmux $x, tmux $'kill-server': the command is not visible
			return strings.Join(all, " ")
		}
		for _, cmd := range tmuxSequence(args[i:]) {
			if tmuxCommand(cmd, other, depth) {
				return strings.Join(all, " ")
			}
		}
		return ""
	}
	return ""
}

// tmuxSequence splits tmux's own command sequence (ls \; kill-server, also one
// quoted argument 'ls ; kill-server') into commands.
func tmuxSequence(args []string) [][]string {
	if len(args) == 1 {
		var out [][]string
		for _, part := range strings.Split(args[0], ";") {
			if f := strings.Fields(part); len(f) > 0 {
				out = append(out, f)
			}
		}
		return out
	}
	var out [][]string
	var cur []string
	for _, a := range args {
		end := strings.HasSuffix(a, ";")
		if a = strings.TrimSuffix(a, ";"); a != "" {
			cur = append(cur, a)
		}
		if end && len(cur) > 0 {
			out, cur = append(out, cur), nil
		}
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// tmuxCommand reports whether one tmux command (name first) is refused.
func tmuxCommand(cmd []string, other bool, depth int) bool {
	args := cmd[1:]
	for _, a := range args { // #( ) in any format string runs a shell command
		if i := strings.Index(a, "#("); i >= 0 && tmuxKill(strings.TrimSuffix(a[i+2:], ")"), depth+1) != "" {
			return true
		}
	}
	names := tmuxCandidates(cmd[0])
	for _, name := range names {
		switch name {
		case "kill-server", "source-file":
			return true
		case "kill-session", "kill-window", "kill-pane":
			if other || tmuxFlagged(args, "at") {
				return true
			}
		case "respawn-pane":
			if tmuxFlagged(args, "k") && (other || tmuxFlagged(args, "t")) {
				return true
			}
		case "send-keys":
			if other || tmuxFlagged(args, "t") {
				for _, a := range args {
					l := strings.ToLower(a)
					if strings.Contains(l, "exit") || strings.Contains(l, "c-d") || tmuxKill(a, depth+1) != "" {
						return true
					}
				}
			}
		case "respawn-window":
			if tmuxFlagged(args, "k") && (other || tmuxFlagged(args, "t")) {
				return true
			}
		case "detach-client":
			if tmuxFlagged(args, "a") {
				return true
			}
		case "new-window", "new-session", "split-window", "pipe-pane", "set-hook", "bind-key",
			"confirm-before", "command-prompt", "display-menu", "display-popup":
			// the command is an argument (shell text, or a tmux command)
			for _, a := range args {
				if strings.HasPrefix(a, "-") {
					continue
				}
				if tmuxKill(a, depth+1) != "" || tmuxKill("tmux "+a, depth+1) != "" {
					return true
				}
			}
		case "run-shell", "if-shell":
			for _, a := range args {
				if strings.HasPrefix(a, "-") {
					continue
				}
				// run-shell takes a shell command, if-shell also tmux commands
				if tmuxKill(a, depth+1) != "" || tmuxKill("tmux "+a, depth+1) != "" {
					return true
				}
			}
		}
	}
	return false
}

// tmuxFlagged reports whether args carry any of the flag letters, alone or
// combined (-t, -a, -at, -t=x, -tx).
func tmuxFlagged(args []string, letters string) bool {
	for _, f := range args {
		if strings.HasPrefix(f, "-") && !strings.HasPrefix(f, "--") && strings.ContainsAny(f[1:], letters) {
			return true
		}
	}
	return false
}

// tmuxSinkFed reports whether a shell/interpreter command (its arguments are
// rest) runs tmux-and-kill text: given inline (-c, -e: the arguments name
// both), read from stdin (no file argument: the line names both), or a file
// that a segment wrote.
func tmuxSinkFed(rest []string, ctx tmuxCtx) bool {
	text := strings.Join(rest, " ")
	if tmuxWord.MatchString(text) && killWord.MatchString(text) {
		return true
	}
	for _, a := range rest {
		if a != "" && !strings.HasPrefix(a, "-") {
			return ctx.written
		}
	}
	return ctx.whole
}
