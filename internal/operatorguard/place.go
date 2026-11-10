package operatorguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/example-user/commander/internal/managerguard"
)

// PlacePolicyFile is the user's own allow-list on the Mac, beside the
// Commander home: what a read-only place run may run and read. Only the user
// writes it; the guard refuses any command naming it, and nothing the server
// sends adds to it.
const PlacePolicyFile = "place-policy.toml"

// PlacePolicy is the parsed policy file.
//
//	read_dirs = ["~/Documents/reports"]
//	[[cli]]
//	name = "aws"
//	read = ["s3 ls", "sts get-caller-identity"]
//	production = true
type PlacePolicy struct {
	ReadDirs []string   `toml:"read_dirs"`
	CLIs     []PlaceCLI `toml:"cli"`
	home     string     // for ~ in ReadDirs
}

// PlaceCLI is one allowed CLI and the read subcommands it may run.
type PlaceCLI struct {
	Name       string   `toml:"name"`
	Read       []string `toml:"read"`       // subcommand prefixes, e.g. "s3 ls"
	Production bool     `toml:"production"` // needs a prod-gate on the run
}

// LoadPlacePolicy reads the policy file. A missing file is the empty policy
// (pure reads in the run directory only); an unreadable or malformed one is an
// error, and the guard then refuses everything.
func LoadPlacePolicy(commanderHome, userHome string) (PlacePolicy, error) {
	p := PlacePolicy{home: userHome}
	b, err := os.ReadFile(filepath.Join(commanderHome, PlacePolicyFile))
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	md, err := toml.Decode(string(b), &p)
	if err != nil {
		return p, fmt.Errorf("%s: %w", PlacePolicyFile, err)
	}
	if un := md.Undecoded(); len(un) > 0 {
		return p, fmt.Errorf("%s: unknown key %s", PlacePolicyFile, un[0])
	}
	for _, c := range p.CLIs {
		if c.Name == "" || strings.ContainsAny(c.Name, "/ ") || len(c.Read) == 0 {
			return p, fmt.Errorf("%s: each [[cli]] needs a bare name and its read subcommands", PlacePolicyFile)
		}
	}
	p.home = userHome
	return p, nil
}

// PlaceRun is what the guard knows of the run it judges for.
type PlaceRun struct {
	Dir      string // the run directory (cwd): the only place a command may write
	ProdGate bool   // the run carries a prod-gate: production CLIs' read subcommands pass
	Home     string // the user's home, for ~ and the credential paths
}

// placeRefused are commands a read-only place run never runs, whatever the policy says.
var placeRefused = map[string]string{
	"env":  "changing the environment",
	"sudo": "sudo", "su": "sudo", "doas": "sudo",
	"launchctl": "launchd", "defaults": "system settings", "security": "the Keychain", "softwareupdate": "installs",
	"ssh": "SSH", "scp": "SSH", "sftp": "SSH", "rsync": "SSH", "ssh-add": "SSH", "ssh-keygen": "SSH",
	"claude": "a nested agent", "codex": "a nested agent", "opencode": "a nested agent",
	"osascript": "AppleScript", "open": "opening apps", "shortcuts": "Shortcuts",
	"brew": "installs", "port": "installs", "pip": "installs", "pip3": "installs", "pipx": "installs", "gem": "installs",
	"cargo": "installs", "npm": "installs", "npx": "installs", "pnpm": "installs", "yarn": "installs", "bun": "installs",
	"python": "an interpreter", "python3": "an interpreter", "node": "an interpreter", "ruby": "an interpreter",
	"perl": "an interpreter", "php": "an interpreter", "lua": "an interpreter", "deno": "an interpreter",
	"bash": "a shell", "sh": "a shell", "zsh": "a shell", "dash": "a shell", "fish": "a shell", "eval": "a shell", "exec": "a shell",
	"source": "a shell", ".": "a shell", "xargs": "running other commands",
	"nc": "the network", "ncat": "the network", "telnet": "the network", "socat": "the network",
	"rm": "a write", "mv": "a write", "cp": "a write", "mkdir": "a write", "touch": "a write", "chmod": "a write",
	"chown": "a write", "ln": "a write", "tee": "a write", "dd": "a write", "kill": "processes", "pkill": "processes", "killall": "processes",
	// The working directory and the environment stay as the app set them:
	// relative writes are judged against the run directory, and the run verbs
	// read the run directory from the environment.
	"cd": "changing directory", "pushd": "changing directory", "popd": "changing directory",
	"export": "changing the environment", "unset": "changing the environment", "declare": "changing the environment",
	"typeset": "changing the environment", "readonly": "changing the environment", "local": "changing the environment",
	"set": "changing the shell", "alias": "changing the shell", "builtin": "a shell", "command": "a shell", "enable": "a shell",
	// Builtins that assign variables or change the shell (read FOO <<< x
	// would rewrite COMMANDER_PLACE_URL for the run verbs).
	"read": "changing the environment", "mapfile": "changing the environment", "readarray": "changing the environment",
	"getopts": "changing the environment", "let": "changing the environment", "vared": "changing the environment",
	"print": "changing the environment", "trap": "changing the shell", "ulimit": "changing the shell", "umask": "changing the shell",
	"shopt": "changing the shell", "setopt": "changing the shell", "unsetopt": "changing the shell", "emulate": "changing the shell",
	"zmodload": "changing the shell", "autoload": "changing the shell", "fc": "changing the shell", "hash": "changing the shell",
	"zle": "changing the shell", "bindkey": "changing the shell", "functions": "changing the shell", "unfunction": "changing the shell",
}

// network commands pass only when the policy names them (and then by subcommand).
var placeNetwork = map[string]bool{"curl": true, "wget": true, "gh": true, "git": false}

// placeSecretPaths are refused by name anywhere in a command.
var placeSecretPaths = []string{
	".ssh", ".aws/credentials", ".aws/config", ".gnupg", ".netrc", ".npmrc", ".pypirc", ".docker/config.json",
	"Library/Keychains", "Keychains", "Library/Cookies", "Library/Application Support/Google/Chrome",
	"Library/Application Support/Firefox", "Library/Safari", "Library/Application Support/Arc",
	"Library/Application Support/BraveSoftware", "Library/Application Support/Microsoft Edge",
	".config/gh", ".kube/config", ".claude", ".codex", ".commander/api-token", "api-token", "device-key",
	PlacePolicyFile,
}

// ClassifyPlace judges command for a read-only place run. It fails closed: a
// policy error or anything it cannot place refuses. Pass: a line whose every
// command is a pure read (the Manager guard's read lists, minus the network),
// or a policy CLI with one of its read subcommands (production ones only with
// a prod-gate on the run), with paths inside the run directory or a policy
// read_dir and writes (redirects) only into the run directory.
func ClassifyPlace(command string, p PlacePolicy, policyErr error, run PlaceRun) Verdict {
	if policyErr != nil {
		return Verdict{Deny: true, Why: "the place policy could not be read (" + policyErr.Error() + "): every command is refused until the user fixes it"}
	}
	if v := Classify(command); v.Deny {
		return v
	}
	if p := secretPath(command); p != "" {
		return Verdict{Deny: true, Why: "names a protected path on this Mac: " + p, Credential: true}
	}
	return p.classifyLine(command, run)
}

func secretPath(command string) string {
	for _, s := range placeSecretPaths {
		if strings.Contains(command, s) {
			return s
		}
	}
	return ""
}

func (p PlacePolicy) classifyLine(line string, run PlaceRun) Verdict {
	if strings.Contains(line, "COMMANDER_PLACE_") {
		// The run verbs read the run's URL, token and directory from the
		// environment the app set: nothing on the line may name them.
		return Verdict{Deny: true, Why: "names the run's own environment (COMMANDER_PLACE_*)"}
	}
	segs, why := lexPlace(line)
	if why != "" {
		return Verdict{Deny: true, Why: "the guard reads only literal commands, and this has " + why}
	}
	for _, seg := range segs {
		for _, w := range seg.Words {
			if !assignWord(w) {
				break
			}
			// FOO=x cmd, or a bare FOO=x (which rewrites an exported
			// variable such as COMMANDER_PLACE_DIR for later commands).
			return Verdict{Deny: true, Why: "sets a variable (" + w + "): a read-only run keeps the environment the app set"}
		}
		for _, w := range append(append(append([]string{}, seg.Words...), seg.Out...), seg.In...) {
			if s := secretPath(w); s != "" {
				return Verdict{Deny: true, Why: "names a protected path on this Mac: " + s, Credential: true}
			}
		}
		for _, r := range seg.Out {
			if r == "/dev/null" {
				continue
			}
			if !run.inDir(r) {
				return Verdict{Deny: true, Why: "writes outside the run directory: " + r}
			}
		}
		for _, r := range seg.In {
			if why := run.checkPath(r, p); why != "" {
				return Verdict{Deny: true, Why: why}
			}
		}
		if v := p.classifyWords(seg.Words, false, run); v.Deny {
			return v
		}
	}
	return Verdict{}
}

func (p PlacePolicy) classifyWords(words []string, hasSubs bool, run PlaceRun) Verdict {
	if len(words) == 0 {
		return Verdict{}
	}
	deny := func(why string) Verdict { return Verdict{Deny: true, Why: why + ": " + strings.Join(words, " ")} }
	// Judge original command/wrapper tokens before Unwrap can expand env -S.
	// Expanded argv lengths cannot identify positions in the original words.
	var refused string
	w := managerguard.UnwrapChecking(words, func(cmd string) bool {
		refused = placeRefused[cmd]
		return refused != ""
	})
	if refused != "" {
		return deny("refused on the Mac (" + refused + ")")
	}
	if len(w) == 0 {
		return Verdict{}
	}
	cmd, args := managerguard.BaseName(w[0]), w[1:]
	if cmd == "cmdr" || cmd == "commander" {
		// Only the run's own verbs: report, comment, mail its Manager. They
		// read the run directory and token from the environment, so they run
		// bare: no wrapper or assignment in front.
		if len(w) != len(words) {
			return deny("cmdr place runs bare on the Mac (no wrapper or variable in front)")
		}
		if len(args) > 1 && args[0] == "place" {
			switch args[1] {
			case "report", "comment", "mail", "revision":
				if why := run.checkPlaceVerb(args[1], args[2:], p); why != "" {
					return deny(why)
				}
				return Verdict{}
			}
		}
		return deny("only cmdr place report|comment|mail run on the Mac")
	}
	for _, a := range args {
		if v := run.checkPath(a, p); v != "" {
			return deny(v)
		}
	}
	if cli, ok := p.cli(cmd); ok {
		if cli.Production && !run.ProdGate {
			return deny("a production CLI, and this run has no prod-gate")
		}
		if !cli.allows(args) {
			return deny("not one of the read subcommands the policy lists for " + cmd)
		}
		if cmd == "curl" || cmd == "wget" {
			if v := managerguard.PureSegment(w, false); !v {
				return deny("a network call that is not a plain GET")
			}
		}
		return Verdict{}
	}
	if placeNetwork[cmd] {
		return deny("the network, and the policy does not list " + cmd)
	}
	if cmd == "git" && len(args) > 0 {
		sub, _ := gitSubcommand(args)
		switch sub {
		case "fetch", "pull", "push", "clone", "ls-remote", "remote", "submodule":
			return deny("git over the network")
		}
	}
	if !managerguard.PureSegment(words, hasSubs) {
		return deny("not a read the guard knows, and not in the place policy")
	}
	return Verdict{}
}

func (p PlacePolicy) cli(name string) (PlaceCLI, bool) {
	for _, c := range p.CLIs {
		if c.Name == name {
			return c, true
		}
	}
	return PlaceCLI{}, false
}

// allows reports whether args start with one of the read subcommands and set
// no flag that changes the method, sends data or writes. curl and wget are
// judged by an allow-list of flags (attached forms such as -XDELETE or
// -Tfile are flags it does not list); any other CLI by a deny-list that also
// catches attached values.
func (c PlaceCLI) allows(args []string) bool {
	switch c.Name {
	case "curl":
		if !curlArgsRead(args) {
			return false
		}
	case "wget":
		if !wgetArgsRead(args) {
			return false
		}
	default:
		for _, a := range args {
			if writeFlag(a) {
				return false
			}
		}
	}
	for _, r := range c.Read {
		want := strings.Fields(r)
		if len(want) <= len(args) && equalWords(args[:len(want)], want) {
			return true
		}
	}
	return false
}

// writeFlag is a flag that sets the method, sends data or a file, or writes
// output, in any spelling: -X POST, -XPOST, --request=POST, -d@f, -Tfile.
func writeFlag(a string) bool {
	l := strings.ToLower(a)
	if strings.HasPrefix(l, "--") {
		for _, f := range []string{"--request", "--method", "--data", "--input", "--output", "--field", "--raw-field",
			"--form", "--upload", "--json", "--body", "--file", "--config"} {
			if strings.HasPrefix(l, f) {
				return true
			}
		}
		return false
	}
	return len(l) > 1 && l[0] == '-' && strings.IndexByte("xtdfok", l[1]) >= 0
}

// curl read flags: blank-separated ones taking a value, and single letters
// that may be grouped (-fsSL).
var (
	curlValueFlags = map[string]bool{"-H": true, "--header": true, "-A": true, "--user-agent": true, "-m": true,
		"--max-time": true, "--connect-timeout": true, "--retry": true}
	curlBareFlags = map[string]bool{"--silent": true, "--show-error": true, "--fail": true, "--fail-with-body": true,
		"--location": true, "--include": true, "--head": true, "--compressed": true, "--verbose": true, "-G": true, "--get": true}
)

func curlArgsRead(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case curlValueFlags[a]:
			i++ // its value; @file reads a local file into the request
			if i < len(args) && strings.HasPrefix(args[i], "@") {
				return false
			}
		case curlBareFlags[a]:
		case len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.Trim(a[1:], "sSfLiIv") == "":
		case strings.HasPrefix(a, "https://") || strings.HasPrefix(a, "http://"):
		default:
			return false // another flag, an attached value, or a non-HTTP URL (file://)
		}
	}
	return true
}

func wgetArgsRead(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-O" && i+1 < len(args) && args[i+1] == "-":
			i++
		case a == "-O-" || a == "-q" || a == "--quiet" || a == "-nv" || a == "-S" || a == "--server-response" || a == "--spider":
		case strings.HasPrefix(a, "--header="):
		case strings.HasPrefix(a, "https://") || strings.HasPrefix(a, "http://"):
		default:
			return false
		}
	}
	return true
}

func equalWords(a, b []string) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func assignWord(w string) bool {
	i := strings.IndexByte(w, '=')
	if i <= 0 {
		return false
	}
	for j, c := range w[:i] {
		if !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || j > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// checkPlaceVerb judges a cmdr place verb's arguments: every one like any
// read, and a report's file only inside the run directory (never a read_dir:
// the report is sent to the server).
func (r PlaceRun) checkPlaceVerb(verb string, args []string, p PlacePolicy) string {
	for i, a := range args {
		if verb != "report" {
			// Text goes to the server: no variable may expand into it (a
			// command substitution is judged as its own command).
			if strings.ContainsAny(a, "$`") {
				return "a variable in text sent to the server (" + a + ")"
			}
			continue
		}
		if why := r.checkPath(a, p); why != "" {
			return why
		}
		f := ""
		switch {
		case (a == "--file" || a == "-file") && i+1 < len(args):
			f = args[i+1]
		case strings.HasPrefix(a, "--file=") || strings.HasPrefix(a, "-file="):
			f = a[strings.IndexByte(a, '=')+1:]
		}
		if f != "" && !r.inDir(f) {
			return "a report is a file in the run directory (" + f + ")"
		}
	}
	return ""
}

// otherUser reports a ~name word (another user's home, or a named directory).
func otherUser(a string) bool {
	return strings.HasPrefix(a, "~") && a != "~" && !strings.HasPrefix(a, "~/")
}

// expand resolves ~ and $HOME in a path argument.
func (r PlaceRun) expand(a string) string {
	switch {
	case a == "~" || a == "$HOME":
		return r.Home
	case strings.HasPrefix(a, "~/"):
		return filepath.Join(r.Home, a[2:])
	case strings.HasPrefix(a, "$HOME/"):
		return filepath.Join(r.Home, a[6:])
	case strings.HasPrefix(a, "${HOME}/"):
		return filepath.Join(r.Home, a[8:])
	}
	return a
}

func under(path, dir string) bool {
	if dir == "" {
		return false
	}
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// tilde expands a leading ~ or ~/ as the shell does. The lexer lets no
// variable through, so a $ left in a word is a literal character.
func (r PlaceRun) tilde(a string) string {
	switch {
	case a == "~":
		return r.Home
	case strings.HasPrefix(a, "~/"):
		return filepath.Join(r.Home, a[2:])
	}
	return a
}

// resolve makes an argument an absolute, clean path: a leading ~ expanded, a
// relative one joined to the run directory.
func (r PlaceRun) resolve(a string) (string, bool) {
	// Refuse parent traversal before Join/Clean can erase it. In particular,
	// link/.. follows link before climbing on the kernel, unlike Clean.
	for _, component := range strings.Split(a, string(filepath.Separator)) {
		if component == ".." {
			return "", false
		}
	}
	t := r.tilde(a)
	if !filepath.IsAbs(t) {
		t = filepath.Join(r.Dir, t)
	}
	return filepath.Clean(t), true
}

// realish resolves the symlinks of the longest existing prefix of a clean
// absolute path and keeps the rest as written.
func realish(path string) string {
	rest := ""
	for p := path; ; p = filepath.Dir(p) {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(real, rest)
		}
		if p == filepath.Dir(p) {
			return path
		}
		rest = filepath.Join(filepath.Base(p), rest)
	}
}

// within reports whether the resolved path is inside dir, both as written and
// with symlinks resolved.
func within(path, dir string) bool {
	return under(path, dir) && under(realish(path), realish(dir))
}

// inDir reports whether a write target is inside the run directory, judged
// after the target is resolved (./.., a/../.., symlinks).
func (r PlaceRun) inDir(target string) bool {
	if otherUser(target) {
		return false
	}
	t, ok := r.resolve(target)
	return ok && within(t, r.Dir)
}

// checkPath judges one argument after resolving it: it must sit in the run
// directory or a policy read_dir (a relative word resolves inside the run
// directory unless it climbs out).
func (r PlaceRun) checkPath(a string, p PlacePolicy) string {
	if i := strings.IndexByte(a, '='); i > 0 && strings.HasPrefix(a, "-") {
		a = a[i+1:] // --file=/path
	} else if len(a) > 2 && a[0] == '-' && a[1] != '-' {
		// An attached short-option value (-C/path, -C$HOME/x, -f~/x, -C..):
		// judged as a path whenever it could be one.
		v := a[2:]
		if !strings.ContainsAny(v, "/~$`") && !strings.HasPrefix(v, "..") {
			return ""
		}
		a = v
	}
	if otherUser(a) {
		return "another user's home or a named directory (" + a + ")"
	}
	x, ok := r.resolve(a)
	if !ok {
		return "a path the guard cannot resolve (" + a + ")"
	}
	if x == "/dev/null" || within(x, r.Dir) {
		return ""
	}
	for _, d := range p.ReadDirs {
		if dd := r.expand(d); filepath.IsAbs(dd) && within(x, filepath.Clean(dd)) {
			return ""
		}
	}
	if !filepath.IsAbs(r.tilde(a)) {
		return "climbs out of the run directory (" + a + ")"
	}
	return "reads outside the run directory and the policy's read_dirs (" + a + ")"
}

// placeReadTools are Claude's read tools; their path arguments are judged like
// a Bash read's.
var placeReadTools = map[string][]string{
	"Read": {"file_path"}, "Grep": {"path"}, "Glob": {"path"}, "LS": {"path"}, "NotebookRead": {"notebook_path"},
}

// placeQuietTools carry no file or network access.
var placeQuietTools = map[string]bool{"TodoWrite": true, "ExitPlanMode": true}

// ClassifyPlaceTool judges any tool call of a read-only place run (the hook
// has no matcher there). Bash goes through ClassifyPlace; read tools by their
// paths; edit tools, subagents, web tools, MCP tools and anything unknown are
// refused.
func ClassifyPlaceTool(tool string, input map[string]any, command string, p PlacePolicy, policyErr error, run PlaceRun) Verdict {
	if policyErr != nil {
		return Verdict{Deny: true, Why: "the place policy could not be read (" + policyErr.Error() + "): every tool is refused until the user fixes it"}
	}
	switch {
	case command != "":
		return ClassifyPlace(command, p, nil, run)
	case placeQuietTools[tool]:
		return Verdict{}
	}
	keys, ok := placeReadTools[tool]
	if !ok {
		return Verdict{Deny: true, Why: "the " + tool + " tool is not allowed in a read-only run on the Mac"}
	}
	for _, k := range keys {
		v, _ := input[k].(string)
		if v == "" {
			continue
		}
		if s := secretPath(v); s != "" {
			return Verdict{Deny: true, Why: "names a protected path on this Mac: " + s, Credential: true}
		}
		if why := run.checkPath(v, p); why != "" {
			return Verdict{Deny: true, Why: why}
		}
	}
	if pat, _ := input["pattern"].(string); tool == "Glob" && (filepath.IsAbs(run.expand(pat)) || strings.Contains(pat, "..")) {
		if why := run.checkPath(pat, p); why != "" || strings.Contains(pat, "..") {
			return Verdict{Deny: true, Why: "a glob outside the run directory and the policy's read_dirs (" + pat + ")"}
		}
	}
	return Verdict{}
}

// PlaceHookOutput is the PreToolUse refusal on the Mac: why, and what a
// read-only run may do instead.
func PlaceHookOutput(why string) []byte {
	return hookOutput("Refused on the user's Mac (read-only run): " + why + ". This run may only read inside its run directory " +
		"and the place policy's read_dirs, run the policy's read subcommands, and report with cmdr place report|comment|mail. " +
		"If the brief needs more, say so in a cmdr place mail to your Manager; do not look for another way.")
}
