package managerguard

import (
	"regexp"
	"strings"
)

// allPureReads reports whether every command of line only reads: the
// command words are on the read lists below, output redirects go to /dev/null,
// and every $(...) is itself pure. Such a line is allowed even from a
// worktree, with cd or git -C into one: reading a worktree changes nothing.
// Anything unknown is not pure, so the usual rules apply.
func allPureReads(line string, depth int) bool {
	if depth > 4 {
		return false
	}
	for _, seg := range parse(line) {
		if !pureSegment(seg.words, len(seg.subs) > 0) {
			return false
		}
		for _, r := range seg.redirects {
			if !strings.HasPrefix(r, "&") && r != "/dev/null" {
				return false
			}
		}
		for _, sub := range seg.subs {
			if !allPureReads(sub, depth+1) {
				return false
			}
		}
	}
	return true
}

// pureCommands read files or process state and never write (find, sed, sort, tmux are checked apart).
var pureCommands = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "grep": true, "egrep": true, "fgrep": true, "rg": true,
	"jq": true, "date": true, "wc": true, "sleep": true, "stat": true, "file": true, "du": true, "df": true,
	"echo": true, "printf": true, "pwd": true, "cd": true, "pushd": true, "popd": true, "test": true, "[": true, "[[": true,
	"true": true, "false": true, ":": true, "sort": true, "uniq": true, "cut": true, "tr": true, "basename": true,
	"dirname": true, "realpath": true, "readlink": true, "which": true, "type": true, "ps": true, "pgrep": true,
	"uptime": true, "id": true, "whoami": true, "hostname": true, "uname": true, "set": true, "export": true,
	"done": true, "fi": true, "}": true, "esac": true, "for": true, "in": true, "break": true, "continue": true, "read": true,
	"column": true, "nl": true, "rev": true, "tac": true, "diff": true, "cmp": true, "comm": true, "md5": true,
	"shasum": true, "lsof": true, "pstree": true,
}

// riskyEnv are assignments that make a read command run something else.
var promptExpansion = regexp.MustCompile(`\$\{[^}]*@[Pp]`)

var riskyEnv = regexp.MustCompile(`^(GIT_|LD_|DYLD_|PAGER=|LESS|BASH_ENV=|ENV=|PATH=)`)

// assignWord is NAME=value or NAME[sub]=value.
var assignWord = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\[[^\]]*\])?\+?=`)

// expandsLater reports a word the shell may evaluate again later: a variable
// assigned a value with $(, a backtick or [ (arithmetic subscripts, PS4 and
// prompt strings evaluate it), PS4 itself, xtrace, printf -v, declare -n.
func expandsLater(words []string, hasSubs bool) bool {
	for _, w := range words {
		if assignWord.MatchString(w) {
			v := w[strings.Index(w, "=")+1:]
			// a real $(...) was parsed and is itself checked; a quoted one is only text to evaluate later
			if !hasSubs && (strings.Contains(v, "$(") || strings.Contains(v, "`")) || strings.Contains(v, "[") {
				return true
			}
		}
		if strings.Contains(w, "PS4") || strings.Contains(w, "xtrace") {
			return true
		}
	}
	w := unwrap(words, true)
	if len(w) == 0 {
		return false
	}
	switch baseName(w[0]) {
	case "set":
		for _, a := range w[1:] {
			if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "x") {
				return true
			}
		}
	case "printf":
		for _, a := range w[1:] {
			if strings.HasPrefix(a, "-v") {
				return true
			}
		}
	case "declare", "typeset", "local":
		for _, a := range w[1:] {
			if strings.HasPrefix(a, "-") && strings.Contains(a, "n") {
				return true
			}
		}
	}
	return false
}

func pureSegment(words []string, hasSubs bool) bool {
	if expandsLater(words, hasSubs) {
		return false
	}
	for _, w := range leadingAssigns(words) {
		if riskyEnv.MatchString(w) {
			return false
		}
	}
	if timeWrites(words) {
		return false
	}
	for _, w := range words {
		if promptExpansion.MatchString(w) {
			return false // ${X@P} expands a prompt string, which runs $(...) in it
		}
	}
	words = unwrap(words, true)
	if len(words) == 0 {
		return true
	}
	cmd, args := baseName(words[0]), words[1:]
	if HelpRequest(words) {
		return true
	}
	switch cmd {
	case "export", "declare", "typeset", "local", "readonly":
		for _, a := range args {
			if riskyEnv.MatchString(a) {
				return false
			}
		}
		return true
	case "rg":
		for _, a := range args {
			if strings.HasPrefix(a, "--pre") || strings.HasPrefix(a, "--hostname-bin") {
				return false // runs a program
			}
		}
		return true
	case "file":
		for _, a := range args {
			if longOptionOf(a, "--compile", 3) || strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "C") {
				return false // writes magic.mgc
			}
		}
		return true
	case "uniq":
		files := 0
		for _, a := range args {
			if a == "-" || !strings.HasPrefix(a, "-") {
				files++
			}
		}
		return files < 2 // the second operand is an output file
	case "git":
		return pureGit(args)
	case "gh":
		return !classifyGh(args).Deny
	case "curl":
		return !classifyCurl(args).Deny
	case "br", "bd":
		return pureBr(args)
	case "cmdr", "commander":
		return pureCmdr(args)
	case "tmux":
		return pureTmux(args)
	case "sed":
		return pureSed(args)
	case "sort":
		for _, a := range args {
			if longOptionOf(a, "--output", 3) || longOptionOf(a, "--compress-program", 4) ||
				strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "o") {
				return false // writes a file / runs a program
			}
		}
		return true
	case "find":
		for _, a := range args {
			if a == "-delete" || a == "-exec" || a == "-execdir" || a == "-ok" || a == "-okdir" || strings.HasPrefix(a, "-fprint") || strings.HasPrefix(a, "-fls") {
				return false
			}
		}
		return true
	}
	return pureCommands[cmd]
}

// longOptionOf reports whether arg is an abbreviation of the long option full
// (GNU getopt accepts any unambiguous prefix, with at least min characters
// here), with or without =value.
func longOptionOf(arg, full string, min int) bool {
	name, _, _ := strings.Cut(arg, "=")
	return len(name) >= min && strings.HasPrefix(full, name)
}

// timeWrites reports a leading `time -o file` / `time --output...`: the wrapper writes a file.
func timeWrites(words []string) bool {
	if len(words) == 0 || baseName(words[0]) != "time" {
		return false
	}
	for _, a := range words[1:] {
		if !strings.HasPrefix(a, "-") {
			return false
		}
		if a == "-o" || strings.HasPrefix(a, "-o") || longOptionOf(a, "--output", 3) {
			return true
		}
	}
	return false
}

// leadingAssigns are the NAME=value words before the command.
func leadingAssigns(words []string) []string {
	n := 0
	for n < len(words) && envAssign.MatchString(words[n]) {
		n++
	}
	return words[:n]
}

// pureSed accepts sed only in a fixed safe shape: print/quiet style flags and
// a script made of addresses and the commands p P d D n N g G h H x q Q = l y s
// (s flags g p i I m M digits). Anything else (w W e r R f, in-place, a script
// file, an option it does not know) is not pure.
func pureSed(args []string) bool {
	haveScript := false
	positional := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case positional || a == "-" || !strings.HasPrefix(a, "-"):
			if !haveScript {
				haveScript = true
				if !sedScriptPure(a) {
					return false
				}
			}
		case a == "--":
			positional = true
		case strings.HasPrefix(a, "--"):
			name, val, has := strings.Cut(a, "=")
			switch {
			case longOptionOf(name, "--expression", 5):
				if !has {
					i++
					if i >= len(args) {
						return false
					}
					val = args[i]
				}
				haveScript = true
				if !sedScriptPure(val) {
					return false
				}
			case name == "--quiet" || name == "--silent" || name == "--regexp-extended" || name == "--posix" ||
				name == "--null-data" || name == "--separate" || name == "--unbuffered" || name == "--sandbox":
			default:
				return false
			}
		default:
			cluster := a[1:]
			for k := 0; k < len(cluster); k++ {
				c := cluster[k]
				switch c {
				case 'n', 'E', 'r', 's', 'z', 'u':
				case 'e':
					val := cluster[k+1:]
					if val == "" {
						i++
						if i >= len(args) {
							return false
						}
						val = args[i]
					}
					haveScript = true
					if !sedScriptPure(val) {
						return false
					}
					k = len(cluster)
				default:
					return false
				}
			}
		}
	}
	return true
}

// sedScriptPure parses a sed script and accepts it only when every command is
// a read-only one (see pureSed).
func sedScriptPure(sc string) bool {
	i := 0
	// skipDelim skips to the end of text delimited by d, honouring backslashes.
	skipDelim := func(d byte) bool {
		for i < len(sc) {
			switch sc[i] {
			case '\\':
				i += 2
				continue
			case d:
				i++
				return true
			}
			i++
		}
		return false
	}
	for i < len(sc) {
		c := sc[i]
		if c == ' ' || c == '\t' || c == '\n' || c == ';' || c == '{' || c == '}' || c == '!' {
			i++
			continue
		}
		// address
		for i < len(sc) {
			switch {
			case sc[i] >= '0' && sc[i] <= '9' || sc[i] == '$' || sc[i] == ',' || sc[i] == '~' || sc[i] == '+':
				i++
				continue
			case sc[i] == '/':
				i++
				if !skipDelim('/') {
					return false
				}
				for i < len(sc) && (sc[i] == 'I' || sc[i] == 'M') {
					i++
				}
				continue
			case sc[i] == '\\' && i+1 < len(sc):
				d := sc[i+1]
				i += 2
				if !skipDelim(d) {
					return false
				}
				continue
			}
			break
		}
		for i < len(sc) && (sc[i] == ' ' || sc[i] == '!') {
			i++
		}
		if i >= len(sc) {
			return true
		}
		cmd := sc[i]
		i++
		switch cmd {
		case 'p', 'P', 'd', 'D', 'n', 'N', 'g', 'G', 'h', 'H', 'x', '=', 'l', '{', '}', ';', ' ', '\n':
		case 'q', 'Q':
			for i < len(sc) && sc[i] >= '0' && sc[i] <= '9' {
				i++
			}
		case 's', 'y':
			if i >= len(sc) {
				return false
			}
			d := sc[i]
			if d == '\\' || d == '\n' || d == ' ' {
				return false
			}
			i++
			if !skipDelim(d) || !skipDelim(d) {
				return false
			}
			if cmd == 's' {
				for i < len(sc) && strings.IndexByte("gpiImM0123456789", sc[i]) >= 0 {
					i++
				}
			}
		default:
			return false
		}
		// after a command only a separator may follow
		for i < len(sc) && (sc[i] == ' ' || sc[i] == '\t') {
			i++
		}
		if i < len(sc) && sc[i] != ';' && sc[i] != '\n' && sc[i] != '}' && sc[i] != '{' {
			return false
		}
	}
	return true
}

// gitReadSubs are the git subcommands a pure read may use; everything else
// (checkout, switch, restore, reset, clean, ...) is not on the list.
var gitReadSubs = map[string]bool{
	"log": true, "show": true, "diff": true, "status": true, "cherry": true, "rev-parse": true, "ls-files": true,
	"ls-tree": true, "cat-file": true, "blame": true, "shortlog": true, "describe": true, "for-each-ref": true,
	"grep": true, "rev-list": true, "merge-base": true, "show-ref": true, "name-rev": true, "check-ignore": true,
	"merge-tree": true, "ls-remote": true, "count-objects": true, "diff-tree": true, "diff-files": true, "diff-index": true,
}

func pureGit(args []string) bool {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		a := args[0]
		switch {
		case a == "-C" || a == "--git-dir" || a == "--work-tree":
			args = args[min(2, len(args)):]
		case strings.HasPrefix(a, "-c") || strings.HasPrefix(a, "--config-env") || strings.HasPrefix(a, "--exec-path"):
			return false // config values run commands (core.fsmonitor, core.pager, diff.external, ...)
		default:
			args = args[1:]
		}
	}
	if len(args) == 0 {
		return true
	}
	rest := args[1:]
	for _, a := range rest {
		switch {
		// Git grep accepts -O (a pager program) anywhere in a short
		// option cluster, including -nOprogram and -inO program.
		case longOptionOf(a, "--output", 4), strings.HasPrefix(a, "-O"),
			args[0] == "grep" && strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a[1:], "O"),
			longOptionOf(a, "--open-files-in-pager", 4),
			longOptionOf(a, "--upload-pack", 4), longOptionOf(a, "--exec", 4), longOptionOf(a, "--receive-pack", 4),
			longOptionOf(a, "--ext-diff", 4), longOptionOf(a, "--textconv", 4), longOptionOf(a, "--filters", 4),
			strings.Contains(a, "::"):
			return false // writes a file or runs a command
		}
	}
	switch args[0] {
	case "branch":
		if !pureBranch(rest) {
			return false
		}
	case "worktree":
		if len(rest) == 0 || rest[0] != "list" {
			return false
		}
	case "stash":
		if len(rest) == 0 || rest[0] != "list" && rest[0] != "show" {
			return false
		}
	case "symbolic-ref":
		words := 0
		for _, a := range rest {
			if !strings.HasPrefix(a, "-") {
				words++
			} else if a == "-d" || a == "--delete" || a == "-m" {
				return false
			}
		}
		if words > 1 {
			return false // a second word sets the ref
		}
	case "reflog":
		if len(rest) > 0 && (rest[0] == "expire" || rest[0] == "delete") {
			return false
		}
	default:
		if !gitReadSubs[args[0]] {
			return false
		}
	}
	return !classifyGit(args).Deny
}

// branchListFlags are the `git branch` flags that only list; the value flags take the next word.
var branchListFlags = map[string]bool{
	"-a": true, "-r": true, "-v": true, "-vv": true, "-l": true, "--list": true, "--show-current": true, "--all": true,
	"--remotes": true, "--verbose": true, "-i": true, "--ignore-case": true, "--no-color": true, "--color": true,
	"--column": true, "--no-column": true, "--omit-empty": true,
}

var branchValueFlags = map[string]bool{
	"--contains": true, "--no-contains": true, "--merged": true, "--no-merged": true, "--points-at": true,
	"--format": true, "--sort": true, "--abbrev": true,
}

// pureBranch accepts only the listing forms of `git branch`: a bare word would create a branch,
// so it must be a --list pattern.
func pureBranch(args []string) bool {
	list := false
	for _, a := range args {
		if a == "-l" || a == "--list" {
			list = true
		}
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, _, hasValue := strings.Cut(a, "=")
		switch {
		case branchListFlags[a]:
		case branchValueFlags[name]:
			if !hasValue {
				i++
			}
		case strings.HasPrefix(a, "--abbrev=") || strings.HasPrefix(a, "--color="):
		case strings.HasPrefix(a, "-"):
			return false
		default:
			if !list {
				return false
			}
		}
	}
	return true
}

func pureBr(args []string) bool {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		if args[0] == "-C" || args[0] == "--db" {
			args = args[min(1, len(args)):]
		}
		args = args[min(1, len(args)):]
	}
	if len(args) == 0 {
		return true
	}
	switch args[0] {
	case "show", "list", "ready", "blocked", "count", "search", "graph", "orphans", "info", "stats", "lint":
		return true
	case "comments":
		for _, a := range args[1:] {
			switch a {
			case "add", "delete", "remove", "edit", "update":
				return false // flags may precede the subcommand
			}
		}
		return true
	case "dep":
		return len(args) > 1 && (args[1] == "list" || args[1] == "tree" || args[1] == "cycles")
	case "label":
		return len(args) > 1 && args[1] == "list"
	}
	return false
}

// cmdrReadVerbs are cmdr verbs that only report.
var cmdrReadVerbs = map[string]bool{
	"internal-inbox": true, "internal-workstreams": true, "internal-workflow-gaps": true,
}

// cmdrReadSubs are "<verb> <sub>" pairs that only report.
var cmdrReadSubs = map[string]bool{
	"graph check": true, "tab ls": true, "account list": true, "runtime report": true, "internal-gate list": true,
	"internal-slot status": true, "app shot": true, "app status": true, "project list": true, "decisions list": true, "proposals list": true,
}

func pureCmdr(args []string) bool {
	if len(args) > 0 && args[0] == "linear" {
		return len(args) == 2 && args[1] == "status"
	}
	var words []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			words = append(words, a)
		}
		if len(words) == 2 {
			break
		}
	}
	if len(words) == 0 {
		return false
	}
	return cmdrReadVerbs[words[0]] || len(words) == 2 && cmdrReadSubs[words[0]+" "+words[1]]
}

func pureTmux(args []string) bool {
	for _, a := range args {
		if a == ";" || strings.HasSuffix(a, ";") {
			return false // a command sequence: only the first verb would be checked
		}
		if strings.Contains(a, "#(") {
			return false // a format runs a shell command
		}
	}
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-L" || a == "-S" || a == "-f":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			switch a {
			case "ls", "list-sessions", "list-windows", "list-panes", "list-clients", "capture-pane", "display-message", "show-options", "show-environment", "has-session", "show-messages":
				return true
			}
			return false
		}
	}
	return false
}

// HelpRequest recognizes narrowly formed diagnostic requests. Substitutions
// and redirects remain separate commands/capabilities checked by the caller.
func HelpRequest(words []string) bool {
	if len(words) < 2 {
		return false
	}
	cmd, args := baseName(words[0]), words[1:]
	switch cmd {
	case "pytest", "py.test", "vitest", "jest", "tox", "nox", "cargo":
		return len(args) == 1 && (args[0] == "--help" || args[0] == "-h")
	case "go":
		return args[0] == "help" && (len(args) == 1 || len(args) == 2 && args[1] == "test")
	}
	return false
}
