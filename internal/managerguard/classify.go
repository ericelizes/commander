// Package managerguard enforces "owning is not doing" for Manager roles: a
// classifier for the Bash commands a Manager may run, the Claude PreToolUse
// hook output, and the refusal log / mail dedupe behind it.
package managerguard

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/example-user/commander/internal/ownercompat"
)

// DenyReason is what a refused Manager sees.
const DenyReason = "Managers do not do the work. Hand this to an Operator: cmdr internal-spawn-operator --project <p> --step <step> --title ... --prompt ..."

// Verdict is the classifier's answer for one Bash command.
type Verdict struct {
	Deny bool
	// Why says which rule fired (for the refusal log); empty when allowed.
	Why string
}

// IsManagerRole reports whether role is a workstream Manager (not the
// Commander, not an Operator).
func IsManagerRole(role string) bool { return strings.HasPrefix(role, "manager-") }

// HookOutput is the Claude Code PreToolUse JSON that refuses a tool call.
func HookOutput() []byte { return hookOutput(DenyReason) }

// TmuxHookOutput refuses a command that kills tmux.
func TmuxHookOutput() []byte { return hookOutput(TmuxKillReason) }

func hookOutput(reason string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keep <p> readable
	_ = enc.Encode(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":            "PreToolUse",
		"permissionDecision":       "deny",
		"permissionDecisionReason": reason,
	}})
	return bytes.TrimSpace(buf.Bytes())
}

// UpdateAPIDenyReason is what an agent sees for a command that touches the app's install endpoints.
const UpdateAPIDenyReason = "Installing Commander is the user's decision: the app's install endpoints (/api/update) are not for agents. Ask the user through Commander."

// TouchesUpdateAPI reports whether command names the app's install endpoints
// (/api/update, /api/update/install). A speed bump only: an agent can build the
// URL in a script file or another tool. The real protection is the native
// confirmation cmdrd shows the person; this just stops the casual and the
// accidental. The command is normalized first (shell quoting, backslashes,
// $'...' escapes, spaces removed) and matched on the "/api/up" prefix; a
// network tool aimed at cmdrd's port or api-url that mentions update is refused
// too.
func TouchesUpdateAPI(command string) bool {
	flat := strings.ToLower(decodeANSIC(command))
	squashed := strings.NewReplacer("'", "", `"`, "", "\\", "", " ", "", "\t", "", "\n", "", "`", "").Replace(flat)
	if strings.Contains(squashed, "/api/up") || strings.Contains(squashed, "api/update") {
		return true
	}
	return networkTool.MatchString(flat) && cmdrdTarget.MatchString(flat) && strings.Contains(squashed, "update")
}

var (
	networkTool = regexp.MustCompile(`\b(curl|wget|http|https|python[0-9.]*|node|nc|ncat|socat|fetch)\b`)
	cmdrdTarget = regexp.MustCompile(`\b8787\b|api-url|api-token`)
	ansiEscape  = regexp.MustCompile(`\$'((?:[^'\\]|\\.)*)'`)
)

// decodeANSIC expands bash $'...' strings (\xHH, \NNN octal, \uHHHH, \n \t)
// so a hex-escaped letter cannot hide the URL.
func decodeANSIC(s string) string {
	return ansiEscape.ReplaceAllStringFunc(s, func(m string) string {
		body := m[2 : len(m)-1]
		var b strings.Builder
		for i := 0; i < len(body); i++ {
			c := body[i]
			if c != '\\' || i+1 >= len(body) {
				b.WriteByte(c)
				continue
			}
			i++
			switch body[i] {
			case 'x', 'u', 'U':
				max := map[byte]int{'x': 2, 'u': 4, 'U': 8}[body[i]]
				j, v := i+1, 0
				for ; j < len(body) && j-i-1 < max && isHex(body[j]); j++ {
					v = v*16 + hexVal(body[j])
				}
				if j == i+1 {
					b.WriteByte(body[i])
				} else {
					b.WriteRune(rune(v))
					i = j - 1
				}
			case '0', '1', '2', '3', '4', '5', '6', '7':
				j, v := i, 0
				for ; j < len(body) && j-i < 3 && body[j] >= '0' && body[j] <= '7'; j++ {
					v = v*8 + int(body[j]-'0')
				}
				b.WriteRune(rune(v))
				i = j - 1
			case 'n', 't':
				b.WriteByte(' ')
			default:
				b.WriteByte(body[i])
			}
		}
		return b.String()
	})
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func hexVal(c byte) int {
	switch {
	case c >= 'a':
		return int(c-'a') + 10
	case c >= 'A':
		return int(c-'A') + 10
	}
	return int(c - '0')
}

// Classify decides whether a Manager may run command from cwd (cwd may be "").
func Classify(command, cwd string) Verdict {
	if TouchesUpdateAPI(command) {
		return deny("touches the install endpoints: /api/update")
	}
	if why := TmuxKill(command); why != "" {
		return deny("kills tmux: " + why)
	}
	if why := WritesWaitLabel(command); why != "" {
		return deny(WaitLabelWhy + why)
	}
	if why := OpensCockpit(command); why != "" {
		return deny(CockpitWhy + why)
	}
	if !AgentWriteGuardOff() {
		if NamesGuardBypass(command) {
			return deny(BeadsWriteWhy + BypassVerb)
		}
		if verb := BeadsWriteVerb(command); verb != "" {
			return deny(BeadsWriteWhy + verb)
		}
	}
	if NamesProtectedCredential(command) {
		return deny("names a protected file (api-token, linear.key or legacy-owner-aliases)")
	}
	if readsHomeInBulk(command, cwd) {
		return deny("reads the commander home in bulk (a glob or recursive search could hit api-token)")
	}
	if allPureReads(command, 0) {
		return Verdict{}
	}
	if inWorktree(cwd) {
		return deny("cwd is inside a worktree: " + cwd)
	}
	return classify(command, 0)
}

// WaitLabelWhy starts the Why of a refused raw wait-label write.
const WaitLabelWhy = "writes a wait label: "

func deny(why string) Verdict { return Verdict{Deny: true, Why: why} }

func classify(command string, depth int) Verdict {
	if depth > 4 {
		return Verdict{}
	}
	for _, seg := range parse(command) {
		if v := classifySegment(seg, depth); v.Deny {
			return v
		}
		for _, inner := range seg.subs {
			if v := classify(inner, depth+1); v.Deny {
				return v
			}
		}
		for _, r := range seg.redirects {
			if !scratchPath(r) {
				return deny("redirect into " + r)
			}
		}
	}
	return Verdict{}
}

// inWorktree reports whether path has a .worktrees component.
func inWorktree(path string) bool {
	for _, part := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if strings.HasPrefix(part, ".worktrees") { // .worktrees and .worktrees.noindex
			return true
		}
	}
	return false
}

// denyUnlessScratch allows an in-place edit only when it names files and every one is scratch.
func denyUnlessScratch(what string, files []string) Verdict {
	if len(files) == 0 {
		return deny(what)
	}
	for _, f := range files {
		if !scratchPath(f) {
			return deny(what + " on " + f)
		}
	}
	return Verdict{}
}

// sedEdit reports whether sed args edit in place, and the file operands.
func sedEdit(args []string) (bool, []string) {
	inPlace, scripted := false, false
	var operands []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--in-place" || strings.HasPrefix(a, "--in-place="):
			inPlace = true
		case a == "--expression" || a == "--file":
			scripted = true
			i++
		case strings.HasPrefix(a, "--expression=") || strings.HasPrefix(a, "--file="):
			scripted = true
		case strings.HasPrefix(a, "--"):
		case strings.HasPrefix(a, "-") && len(a) > 1:
			for j := 1; j < len(a); j++ {
				if a[j] == 'i' {
					inPlace = true
					break // the rest is the backup suffix
				}
				if a[j] == 'e' || a[j] == 'f' {
					scripted = true
					if j == len(a)-1 {
						i++
					}
					break
				}
			}
		case a == "":
		default:
			operands = append(operands, a)
		}
	}
	if !scripted && len(operands) > 0 {
		operands = operands[1:]
	}
	return inPlace, operands
}

// perlEdit reports whether perl args edit in place, and the file operands.
func perlEdit(args []string) (bool, []string) {
	inPlace, scripted := false, false
	var operands []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case strings.HasPrefix(a, "--"):
		case strings.HasPrefix(a, "-") && len(a) > 1:
			for j := 1; j < len(a); j++ {
				if a[j] == 'i' {
					inPlace = true
					break
				}
				if a[j] == 'e' || a[j] == 'E' {
					scripted = true
					if j == len(a)-1 {
						i++
					}
					break
				}
			}
		case a == "":
		default:
			operands = append(operands, a)
		}
	}
	if !scripted && len(operands) > 0 {
		operands = operands[1:]
	}
	return inPlace, operands
}

// scratchPath is somewhere a Manager may write: not the repo.
func scratchPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "&") || strings.HasPrefix(p, "/dev/") {
		return true
	}
	for _, prefix := range []string{"/tmp/", "/private/tmp/", "/var/folders/", "$TMPDIR/", "${TMPDIR}/"} {
		if strings.HasPrefix(p, prefix) && !strings.Contains(p, "..") {
			return true
		}
	}
	return false
}

// shellKeywords start a command without being it: "do git commit" runs git.
var shellKeywords = map[string]bool{"do": true, "then": true, "else": true, "elif": true, "if": true, "while": true, "until": true, "{": true, "!": true, "(": true}

var envAssign = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// wrapperValueFlags are the flags of each wrapper that take the next word as
// their value, so that value is not mistaken for the command.
var wrapperValueFlags = map[string]map[string]bool{
	"env":     {"-u": true, "--unset": true, "-C": true, "--chdir": true, "-P": true},
	"sudo":    {"-u": true, "-g": true, "-C": true, "-h": true, "-p": true, "-r": true, "-t": true, "-D": true, "-U": true, "-R": true, "-T": true},
	"nice":    {"-n": true, "--adjustment": true},
	"time":    {"-f": true, "--format": true, "-o": true, "--output": true},
	"exec":    {"-a": true},
	"command": {},
	"nohup":   {},
	"builtin": {},
	"timeout": {"-s": true, "--signal": true, "-k": true, "--kill-after": true},
}

// unwrap drops the env assignments, wrappers (with their flags and flag
// values), shell keywords and timeout's duration in front of the command. env
// -S "cmd args" is expanded into its words. With pure set, exec is left in
// place (it is not a read).
func unwrap(words []string, pure bool) []string {
	for len(words) > 0 {
		w := words[0]
		b := baseName(w)
		switch {
		case envAssign.MatchString(w):
			words = words[1:]
		case wrapperValueFlags[b] != nil && !(pure && b == "exec") && b != "timeout" || b == "timeout" && len(words) > 1:
			valueFlags := wrapperValueFlags[b]
			words = words[1:]
			for len(words) > 0 && (strings.HasPrefix(words[0], "-") || envAssign.MatchString(words[0])) {
				f := words[0]
				words = words[1:]
				if f == "--" {
					break
				}
				if b == "env" && (f == "-S" || f == "--split-string") && len(words) > 0 {
					words = append(strings.Fields(words[0]), words[1:]...)
					continue
				}
				if b == "env" && strings.HasPrefix(f, "-S") && !strings.HasPrefix(f, "--") {
					words = append(strings.Fields(f[2:]), words...)
					continue
				}
				if b == "env" && strings.HasPrefix(f, "--split-string=") {
					words = append(strings.Fields(strings.TrimPrefix(f, "--split-string=")), words...)
					continue
				}
				if valueFlags[f] && len(words) > 0 {
					words = words[1:]
				}
			}
			if b == "timeout" && len(words) > 0 {
				words = words[1:] // the duration
			}
		case shellKeywords[w] || !pure && strings.HasSuffix(w, "(){"):
			words = words[1:]
		default:
			return words
		}
	}
	return words
}

func classifySegment(seg segment, depth int) Verdict {
	words := unwrap(seg.words, false)
	if len(words) == 0 {
		return Verdict{}
	}
	cmd, args := baseName(words[0]), words[1:]
	if HelpRequest(words) {
		return Verdict{}
	}
	switch cmd {
	case "cmdr", "commander":
		if len(args) > 0 && args[0] == "linear" && !(len(args) == 2 && args[1] == "status") {
			return deny("Linear credential writes belong to the user; only cmdr linear status is read-only")
		}
	case "cd", "pushd":
		for _, a := range args {
			if !strings.HasPrefix(a, "-") && inWorktree(a) {
				return deny("cd into a worktree: " + a)
			}
		}
	case "git":
		return classifyGit(args)
	case "gh":
		return classifyGh(args)
	case "bash", "sh", "zsh", "dash":
		for i, a := range args {
			if strings.HasPrefix(a, "-") && strings.Contains(a, "c") && i+1 < len(args) {
				return classify(args[i+1], depth+1)
			}
		}
	case "sed":
		if inPlace, files := sedEdit(args); inPlace {
			return denyUnlessScratch("sed -i", files)
		}
	case "perl":
		if inPlace, files := perlEdit(args); inPlace {
			return denyUnlessScratch("perl -i", files)
		}
	case "tee":
		for _, a := range args {
			if !strings.HasPrefix(a, "-") && !scratchPath(a) {
				return deny("tee into " + a)
			}
		}
	case "cp", "mv", "rm", "touch", "mkdir", "rmdir", "patch", "truncate", "install", "ln", "chmod", "chown":
		for _, a := range args {
			if !strings.HasPrefix(a, "-") && !scratchPath(a) {
				return deny(cmd + " on " + a)
			}
		}
	case "dd":
		for _, a := range args {
			if strings.HasPrefix(a, "of=") && !scratchPath(strings.TrimPrefix(a, "of=")) {
				return deny("dd of=")
			}
		}
	case "find":
		for _, a := range args {
			if a == "-delete" || strings.HasPrefix(a, "-fprint") || strings.HasPrefix(a, "-fls") {
				return deny("find " + a)
			}
		}
	case "python", "python3", "node", "ruby", "perl5":
		for i, a := range args {
			if (a == "-c" || a == "-e") && i+1 < len(args) && writesFiles(args[i+1]) {
				return deny(cmd + " " + a + " writing files")
			}
		}
	case "pytest", "py.test", "vitest", "jest", "tox", "nox":
		return deny("test runner " + cmd)
	case "go", "cargo":
		if len(args) > 0 && args[0] == "test" {
			return deny(cmd + " test")
		}
	case "npm", "pnpm", "yarn", "bun":
		if len(args) > 0 && (args[0] == "test" || args[0] == "t" || args[0] == "tst" ||
			(len(args) > 1 && (args[0] == "run" || args[0] == "run-script") && strings.HasPrefix(args[1], "test"))) {
			return deny(cmd + " test")
		}
	case "npx":
		for _, a := range args {
			if a == "vitest" || a == "jest" {
				return deny("npx " + a)
			}
		}
	case "make":
		for _, a := range args {
			if a == "test" || strings.HasPrefix(a, "test-") || a == "check" {
				return deny("make " + a)
			}
		}
	case "uv":
		if len(args) > 0 && args[0] == "run" {
			return deny("uv run")
		}
	case "curl":
		return classifyCurl(args)
	}
	return Verdict{}
}

func writesFiles(code string) bool {
	code = strings.NewReplacer("sys.stdout.write", "", "sys.stderr.write", "").Replace(code)
	code = stripReadOpens(code)
	for _, marker := range []string{"write", "open(", "shutil", "os.remove", "os.rename", "unlink", "rmtree", "makedirs", "mkdir", "copyfile", "fs."} {
		if strings.Contains(code, marker) {
			return true
		}
	}
	return false
}

// stripReadOpens removes open(...) calls that name no write mode, so reading a
// file with python is not mistaken for writing one. Only a bare open( counts
// (not popen(, os.open(, io.open(), and the mode must be absent or a quoted
// read-only literal: a mode from a variable is not known to be a read.
func stripReadOpens(code string) string {
	var b strings.Builder
	for {
		i := strings.Index(code, "open(")
		if i < 0 {
			return b.String() + code
		}
		rest := code[i+len("open("):]
		end := strings.Index(rest, ")")
		bare := i == 0 || !isIdentOrDot(code[i-1])
		if bare && end >= 0 && readOnlyOpenArgs(rest[:end]) {
			b.WriteString(code[:i])
		} else {
			b.WriteString(code[:i+len("open(")])
		}
		code = rest
	}
}

func isIdentOrDot(c byte) bool {
	return c == '.' || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// readOnlyOpenArgs: one argument (the path), or the path and a read-only mode literal.
func readOnlyOpenArgs(inner string) bool {
	parts := strings.Split(inner, ",")
	switch len(parts) {
	case 1:
		return !strings.Contains(inner, "=")
	case 2:
		return readMode.MatchString(strings.TrimSpace(parts[1]))
	}
	return false
}

var readMode = regexp.MustCompile(`^["'][rbt]*["']$`)

// modeArg matches a quoted open() mode that writes: "w", 'a+', "x", "r+".
var modeArg = regexp.MustCompile(`["'][rwaxbt]*[wax+][rwaxbt+]*["']`)

func classifyGit(args []string) Verdict {
	// Global options before the subcommand.
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch a := args[0]; {
		case a == "-C" || a == "--git-dir" || a == "--work-tree":
			if len(args) > 1 && inWorktree(args[1]) {
				return deny("git " + a + " into a worktree")
			}
			args = args[min(2, len(args)):]
		case strings.HasPrefix(a, "--git-dir=") || strings.HasPrefix(a, "--work-tree="):
			if inWorktree(a) {
				return deny("git " + a)
			}
			args = args[1:]
		case a == "-c":
			args = args[min(2, len(args)):]
		default:
			args = args[1:]
		}
	}
	if len(args) == 0 {
		return Verdict{}
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "status", "log", "diff", "show", "fetch", "rev-parse", "rev-list", "ls-files", "ls-tree", "blame",
		"describe", "grep", "shortlog", "cat-file", "merge-base", "for-each-ref", "name-rev",
		"show-ref", "ls-remote", "diff-tree", "range-diff", "whatchanged", "version", "help", "count-objects",
		"cherry", "check-ignore", "check-attr", "diff-index", "diff-files", "verify-commit", "var":
		return Verdict{}
	case "reflog":
		if len(rest) == 0 || rest[0] == "show" || rest[0] == "list" || strings.HasPrefix(rest[0], "-") {
			return Verdict{}
		}
		return deny("git reflog " + rest[0])
	case "symbolic-ref":
		positional := 0
		for _, a := range rest {
			switch {
			case a == "-d" || a == "--delete" || a == "-m":
				return deny("git symbolic-ref " + a)
			case !strings.HasPrefix(a, "-"):
				positional++
			}
		}
		if positional > 1 {
			return deny("git symbolic-ref sets a ref")
		}
		return Verdict{}
	case "branch":
		listing := false
		for _, a := range rest {
			switch a {
			case "-l", "--list", "--show-current", "--merged", "--no-merged", "--contains", "--no-contains", "--points-at":
				listing = true
			}
		}
		for _, a := range rest {
			if !strings.HasPrefix(a, "-") {
				if listing {
					continue // a pattern or commit for a listing
				}
				return deny("git branch with a name")
			}
			switch a {
			case "-d", "-D", "-m", "-M", "-c", "-C", "--delete", "--move", "--copy", "-f", "--force", "-u", "--set-upstream-to", "--unset-upstream":
				return deny("git branch " + a)
			}
		}
		return Verdict{}
	case "tag":
		listing := false
		for _, a := range rest {
			switch a {
			case "-l", "--list", "--contains", "--no-contains", "--merged", "--no-merged", "--points-at":
				listing = true
			}
		}
		for _, a := range rest {
			if !strings.HasPrefix(a, "-") {
				if listing {
					continue // a pattern or commit for a listing
				}
				return deny("git tag writes")
			}
			switch a {
			case "-d", "--delete", "-a", "-s", "-f", "--force", "-m":
				return deny("git tag " + a)
			}
		}
		return Verdict{}
	case "remote":
		for _, a := range rest {
			switch a {
			case "add", "remove", "rm", "rename", "set-url", "set-head", "set-branches", "prune", "update":
				return deny("git remote " + a)
			}
		}
		return Verdict{}
	case "stash":
		if len(rest) > 0 && (rest[0] == "list" || rest[0] == "show") {
			return Verdict{}
		}
		return deny("git stash")
	case "worktree":
		if len(rest) > 0 && rest[0] == "list" {
			return Verdict{}
		}
		return deny("git worktree")
	case "config":
		for _, a := range rest {
			if a == "--get" || a == "--get-all" || a == "-l" || a == "--list" {
				return Verdict{}
			}
		}
		return deny("git config writes")
	case "checkout", "switch":
		for _, a := range rest {
			if a == "-b" || a == "-B" || a == "-c" || a == "-C" || a == "--orphan" || a == "--detach" {
				return deny("git " + sub + " " + a)
			}
		}
		return Verdict{}
	}
	return deny("git " + sub)
}

func classifyGh(args []string) Verdict {
	var words []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			words = append(words, a)
		}
	}
	if len(words) == 0 {
		return Verdict{}
	}
	switch words[0] {
	case "api":
		if len(words) > 1 && words[1] == "graphql" {
			return classifyGraphQL(args)
		}
		for i, a := range args {
			if _, ok := ghFieldValue(args, i); ok || a == "--input" || strings.HasPrefix(a, "--input=") {
				return deny("gh api with a body")
			}
			if m, ok := ghMethod(args, i); ok && !strings.EqualFold(m, "GET") {
				return deny("gh api " + m)
			}
		}
		return Verdict{}
	case "search":
		return Verdict{} // every search verb only reads
	}
	// Every other noun (pr, run, issue, repo, release, workflow, ...) reads
	// through the same verbs; anything else (edit, merge, rerun, run, ...) writes.
	if len(words) > 1 && ghReadVerbs[words[1]] {
		return Verdict{}
	}
	return deny("gh " + strings.Join(words, " "))
}

var ghReadVerbs = map[string]bool{"list": true, "view": true, "status": true, "checks": true, "diff": true, "watch": true}

func classifyCurl(args []string) Verdict {
	for i, a := range args {
		switch {
		case a == "-d" || a == "-F" || a == "-T" || a == "--data" || a == "--form" || a == "--upload-file" ||
			strings.HasPrefix(a, "--data-") || strings.HasPrefix(a, "--json"):
			return deny("curl with a body")
		case a == "-X" || a == "--request":
			if i+1 < len(args) && !strings.EqualFold(args[i+1], "GET") {
				return deny("curl " + args[i+1])
			}
		case a == "-o" || a == "-O" || a == "--output" || a == "--remote-name":
			if a == "-O" || a == "--remote-name" || (i+1 < len(args) && !scratchPath(args[i+1])) {
				return deny("curl writing a file")
			}
		}
	}
	return Verdict{}
}

func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// classifyGraphQL allows a GraphQL read. GraphQL always sends its query as a
// field (-f query=...), so a field is not a write here; a mutation is, and a
// query read from a file (--input, query=@file) cannot be checked.
func classifyGraphQL(args []string) Verdict {
	for i, a := range args {
		if a == "--input" || strings.HasPrefix(a, "--input=") {
			return deny("gh api graphql --input")
		}
		if field, ok := ghFieldValue(args, i); ok {
			name, value, _ := strings.Cut(field, "=")
			if name != "query" {
				continue
			}
			if strings.HasPrefix(value, "@") {
				return deny("gh api graphql query from a file")
			}
			if graphqlMutation.MatchString(value) {
				return deny("gh api graphql mutation")
			}
		}
	}
	return Verdict{}
}

// ghFieldValue returns the key=value of a gh api field flag at args[i], in any
// spelling: -f k=v, -fk=v, -F.., --field k=v, --field=k=v, --raw-field (and
// unique prefixes of the long names).
func ghFieldValue(args []string, i int) (string, bool) {
	a := args[i]
	switch {
	case strings.HasPrefix(a, "--"):
		name, val, has := strings.Cut(a, "=")
		if !longOptionOf(name, "--field", 3) && !longOptionOf(name, "--raw-field", 3) {
			return "", false
		}
		if has {
			return val, true
		}
		if i+1 < len(args) {
			return args[i+1], true
		}
		return "", true
	case ghCluster.MatchString(a) && strings.ContainsAny(a[1:], "fF"):
		return a, true
	case strings.HasPrefix(a, "-f") || strings.HasPrefix(a, "-F"):
		if len(a) > 2 {
			return a[2:], true
		}
		if i+1 < len(args) {
			return args[i+1], true
		}
		return "", true
	}
	return "", false
}

// ghMethod returns the HTTP method set by args[i]: -X M, -XM, --method M, --method=M.
func ghMethod(args []string, i int) (string, bool) {
	a := args[i]
	switch {
	case strings.HasPrefix(a, "--"):
		name, val, has := strings.Cut(a, "=")
		if !longOptionOf(name, "--method", 3) {
			return "", false
		}
		if has {
			return val, true
		}
		if i+1 < len(args) {
			return args[i+1], true
		}
		return "?", true
	case strings.HasPrefix(a, "-X"):
		if len(a) > 2 {
			return strings.TrimPrefix(a[2:], "="), true
		}
		if i+1 < len(args) {
			return args[i+1], true
		}
		return "?", true
	case ghCluster.MatchString(a):
		// bundled short flags: -iXPOST means -i -X POST
		if k := strings.IndexByte(a, 'X'); k > 0 {
			if k+1 < len(a) {
				return strings.TrimPrefix(a[k+1:], "="), true
			}
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "?", true
		}
	}
	return "", false
}

// ghCluster is a bundle of short flags (-iXPOST, -sfa=b).
var ghCluster = regexp.MustCompile(`^-[A-Za-z]{2,}`)

var graphqlMutation = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_])mutation([^A-Za-z0-9_]|$)`)

// credentialWord matches literal basenames anywhere, and globs against bare
// basenames or full protected paths. A source-directory glob is not a home glob.
func credentialWord(word string) bool {
	home, _ := os.UserHomeDir()
	commanderHome := os.Getenv("COMMANDER_HOME")
	if commanderHome == "" {
		commanderHome = filepath.Join(home, ".commander")
	}
	expand := strings.NewReplacer("${COMMANDER_HOME}", commanderHome, "$COMMANDER_HOME", commanderHome, "${HOME}", home, "$HOME", home)
	word = expand.Replace(word)
	if word == "~" || strings.HasPrefix(word, "~/") {
		word = home + strings.TrimPrefix(word, "~")
	}
	for _, name := range []string{"api-token", "linear.key", ownercompat.AliasesFile} {
		if !hasGlob(word) && filepath.Base(word) == name {
			return true
		}
		targets := []string{filepath.Join(home, ".commander", name), filepath.Join(commanderHome, name)}
		if !strings.ContainsRune(word, filepath.Separator) {
			targets = append(targets, name)
		}
		for _, target := range targets {
			if matched, _ := filepath.Match(word, target); matched {
				return true
			}
		}
	}
	return false
}

// NamesProtectedCredential reports commands naming private credential files,
// or the legacy owner alias file anywhere in the line (a redirect target too:
// writing it would widen who counts as the user).
func NamesProtectedCredential(command string) bool {
	return strings.Contains(command, ownercompat.AliasesFile) || readsToken(command, 0)
}

// readsToken reports whether any command of line (inside $(...) and sh -c
// too) has a word that names the API token. Agents run as the same user and
// can read it anyway: this is the speed bump for cat/grep/jq/head/tail/less.
func readsToken(line string, depth int) bool {
	if depth > 4 {
		return false
	}
	for _, words := range Commands(line) {
		for i, w := range words {
			if credentialWord(w) {
				return true
			}
			for _, literal := range strings.FieldsFunc(w, func(r rune) bool { return r == '\'' || r == '"' }) {
				if credentialWord(literal) {
					return true
				}
			}
			if strings.HasPrefix(w, "-") && strings.Contains(w, "c") && i > 0 && i+1 < len(words) {
				switch baseName(words[0]) {
				case "bash", "sh", "zsh", "dash":
					if readsToken(words[i+1], depth+1) {
						return true
					}
				}
			}
		}
	}
	return false
}
