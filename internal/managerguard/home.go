package managerguard

import (
	"os"
	"path"
	"regexp"
	"strings"
)

// homeTop matches the top of the commander home itself (~/.commander, $HOME/.commander/,
// $COMMANDER_HOME, or a glob directly in it), not a subdirectory such as ~/.commander/logs.
var homeTop = regexp.MustCompile(`(\.commander|COMMANDER_HOME\}?)(/+[^/]*[*?\[][^/]*)?/*$`)

var bulkReaders = map[string]bool{
	"cat": true, "head": true, "tail": true, "grep": true, "egrep": true, "fgrep": true, "rg": true, "jq": true,
	"less": true, "more": true, "strings": true, "xxd": true, "od": true, "base64": true, "awk": true, "sed": true,
	"cut": true, "sort": true, "tac": true, "nl": true, "wc": true, "rev": true, "diff": true, "comm": true, "column": true,
	"tr": true, "paste": true, "fold": true,
}

func hasGlob(w string) bool { return strings.ContainsAny(w, "*?[") }

// homeish reports a directory that is the commander home itself or the user's
// home or an ancestor of it (/, /home, /Users, ~, ~root, ~/..).
func homeish(dir string) bool {
	d := strings.TrimRight(dir, "/")
	if d == "" || userHomeParent[d] || homeTop.MatchString(d) || d == "/home" || d == "/Users" {
		return true
	}
	return strings.HasPrefix(d, "~") && !strings.Contains(d, "/")
}

// globUnderHome: a glob whose fixed part is the home, a home ancestor or the
// top of the commander home can match a dot-dir (.commander) or api-token.
func globUnderHome(resolved string) bool {
	k := strings.IndexAny(resolved, "*?[")
	if k < 0 {
		return false
	}
	pre := resolved[:k]
	i := strings.LastIndex(pre, "/")
	if i < 0 {
		return false
	}
	return homeish(pre[:i+1])
}

var (
	homeVars       = strings.NewReplacer("${COMMANDER_HOME}", "~/.commander", "$COMMANDER_HOME", "~/.commander", "${HOME}", "~", "$HOME", "~")
	mentionsHome   = regexp.MustCompile(`\.commander|COMMANDER_HOME|\{[^}]*commander|\.\{`)
	userHomeParent = userHomeAncestors()
)

// userHomeAncestors are the user's home dir and every directory above it: a
// recursive reader aimed at one of them walks the commander home too.
func userHomeAncestors() map[string]bool {
	out := map[string]bool{"~": true, "/": true}
	h, err := os.UserHomeDir()
	for err == nil && h != "" {
		out[h] = true
		p := path.Dir(h)
		if p == h {
			break
		}
		h = p
	}
	return out
}

// resolveIn cleans arg (./.. segments, $HOME and $COMMANDER_HOME spellings) and,
// when it is relative, joins it onto cwd (itself already resolved).
func resolveIn(cwd, arg string) string {
	a := homeVars.Replace(arg)
	if a == "" {
		return cwd
	}
	if !strings.HasPrefix(a, "/") && !strings.HasPrefix(a, "~") && cwd != "" {
		a = cwd + "/" + a
	}
	if strings.HasPrefix(a, "~") {
		// keep ~/.. from collapsing to "." : clean under a root, then drop it
		if c := path.Clean("/" + a); c != "/" {
			return c[1:]
		}
		return "/"
	}
	return path.Clean(a)
}

func hasExpansion(w string) bool { return strings.ContainsAny(w, "${`") }

// readsHomeInBulk is the cheap speed bump for item "token by glob": a reader that
// globs over, or searches recursively through, the top of the commander home (or a
// directory above it) could open api-token without naming it. Paths are cleaned
// first and a `cd` earlier in the line moves the working directory. An argument
// spelled with a variable, brace or ANSI-C expansion under the home counts as bulk.
func readsHomeInBulk(line, cwd string) bool {
	vcwd := ""
	if cwd != "" {
		vcwd = path.Clean(cwd)
	}
	mentions := mentionsHome.MatchString(line)
	for _, words := range Commands(line) {
		w := unwrap(words, true)
		if len(w) == 0 {
			continue
		}
		cmd, args := baseName(w[0]), w[1:]
		if cmd == "cd" || cmd == "pushd" {
			dest := "~"
			for _, a := range args {
				if !strings.HasPrefix(a, "-") {
					dest = a
				}
			}
			vcwd = resolveIn(vcwd, dest)
			continue
		}
		// a glob under the home dir (any command) can reach .commander/api-token
		for _, a := range args {
			if !strings.HasPrefix(a, "-") && hasGlob(a) && globUnderHome(resolveIn(vcwd, a)) {
				return true
			}
		}
		if !bulkReaders[cmd] {
			continue
		}
		recursive := cmd == "rg"
		var paths []string
		if cmd == "grep" || cmd == "egrep" || cmd == "fgrep" {
			for _, a := range args {
				if a == "--recursive" || a == "--dereference-recursive" || strings.HasPrefix(a, "--directories=") ||
					a == "-d" || strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a, "rR") {
					recursive = true
				}
			}
		}
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				paths = append(paths, a)
			}
		}
		// grep -r PATTERN / rg PATTERN with no path search the working directory
		if recursive && vcwd != "" && homeish(vcwd) && len(paths) < 2 {
			return true
		}
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				continue
			}
			r := resolveIn(vcwd, a)
			switch {
			case homeTop.MatchString(r) && (recursive || hasGlob(a)):
				return true
			case recursive && homeish(r):
				return true
			case hasExpansion(a):
				// the file name is built by the shell: look at the directory part before it
				pre := homeVars.Replace(a)
				if k := strings.IndexAny(pre, "${`*?["); k >= 0 {
					pre = pre[:k]
				}
				dir, known := "", strings.Contains(pre, "/")
				if known {
					dir = pre[:strings.LastIndex(pre, "/")+1]
				}
				if known && homeTop.MatchString(resolveIn(vcwd, dir)) || !known && homeTop.MatchString(vcwd) {
					return true
				}
				if !known && mentions && (hasGlob(a) || strings.Contains(a, "{")) {
					return true
				}
			}
		}
	}
	return false
}
