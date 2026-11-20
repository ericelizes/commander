// Package harddeny is the short list of commands the Commander and its
// Operators never run. They are refused outright: never prompted, never left
// waiting on an approval. Everything else runs without a prompt.
package harddeny

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/example-user/commander/internal/managerguard"
)

// Context is what the check knows about who runs the command.
type Context struct {
	// Worktree is the agent's own working tree (an Operator's cwd). Empty for
	// the Commander, whose cwd is shared: a recursive delete there is refused.
	Worktree string
	// DeployAllowed: the run is an approved release-deploy step, so the
	// production deploy command may run.
	DeployAllowed bool
	// MainlineAllowed: the run is a release's land-on-mainline step, so it may
	// push to main or master.
	MainlineAllowed bool
}

// Verdict names the rule a command breaks; Rule is empty when it may run.
type Verdict struct {
	Rule string
	Why  string
}

// Denied reports whether the command is refused.
func (v Verdict) Denied() bool { return v.Rule != "" }

// Message is what the refused agent reads.
func (v Verdict) Message() string {
	return "Refused by Commander's hard limits (" + v.Rule + "): " + v.Why + ". The Commander and its agents never run this. " +
		"If it is really needed, raise it as a Commander card for Eric saying why, and carry on with other work."
}

// Check decides whether command may run.
func Check(command string, ctx Context) Verdict {
	if v := checkSQL(command); v.Denied() {
		return v
	}
	if !ctx.MainlineAllowed && forgeMerge(command) {
		return Verdict{Rule: "mainline-push", Why: "merges a pull request on the internal git forge outside a release's land-on-mainline step (an app's mainline changes only through its release process)"}
	}
	return check(command, ctx, 0)
}

// The internal forge's merge endpoint (Gitea: POST .../pulls/N/merge) and the
// tea CLI. Matched on the raw text, so a merge written into a script that is
// copied and run elsewhere is caught too. Nov 20: an agent merged QA Hub PR #1
// to master this way, and only git push was refused.
var (
	pullMergePath = regexp.MustCompile(`/pulls/[^\s'"/]+/merge\b`)
	internalForge = regexp.MustCompile(`forge-git|/api/v1/repos/`)
	teaMerge      = regexp.MustCompile(`\btea\s+(pr|pulls)\s+merge\b`)
)

func forgeMerge(command string) bool {
	return teaMerge.MatchString(command) || (pullMergePath.MatchString(command) && internalForge.MatchString(command))
}

func check(command string, ctx Context, depth int) Verdict {
	if depth > 4 {
		return Verdict{}
	}
	for _, words := range managerguard.Commands(command) {
		if v := checkWords(words, ctx, depth); v.Denied() {
			return v
		}
	}
	return Verdict{}
}

var envAssign = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// strip drops the wrappers that run another command (env, sudo, nohup, timeout…).
func strip(words []string) []string {
	for len(words) > 0 {
		w := base(words[0])
		switch {
		case envAssign.MatchString(words[0]):
			words = words[1:]
		case w == "env" || w == "command" || w == "time" || w == "nohup" || w == "nice" || w == "setsid" || w == "exec":
			words = words[1:]
			for len(words) > 0 && (strings.HasPrefix(words[0], "-") || envAssign.MatchString(words[0])) {
				if words[0] == "-n" && w == "nice" && len(words) > 1 {
					words = words[1:]
				}
				words = words[1:]
			}
		case w == "sudo" || w == "doas":
			words = words[1:]
			for len(words) > 0 && strings.HasPrefix(words[0], "-") {
				if (words[0] == "-u" || words[0] == "-g" || words[0] == "-C" || words[0] == "-h") && len(words) > 1 {
					words = words[1:]
				}
				words = words[1:]
			}
		case w == "timeout":
			words = words[1:]
			for len(words) > 0 && strings.HasPrefix(words[0], "-") {
				words = words[1:]
			}
			if len(words) > 0 {
				words = words[1:] // the duration
			}
		default:
			return words
		}
	}
	return words
}

func checkWords(words []string, ctx Context, depth int) Verdict {
	words = strip(words)
	if len(words) == 0 {
		return Verdict{}
	}
	name, args := base(words[0]), words[1:]
	switch name {
	case "bash", "sh", "zsh", "dash":
		for i, a := range args {
			if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.HasSuffix(a, "c") && i+1 < len(args) {
				return check(args[i+1], ctx, depth+1)
			}
		}
	case "ssh":
		if rest := sshCommand(args); rest != "" {
			if remoteVM(args) {
				if v := vmVerb(rest); v.Denied() {
					return v
				}
			}
			// The far side is another machine: its paths are not this worktree.
			return check(rest, Context{DeployAllowed: ctx.DeployAllowed, MainlineAllowed: ctx.MainlineAllowed}, depth+1)
		}
	case "forge-ssh-proxy":
		if len(args) > 1 {
			return check(strings.Join(args[1:], " "), Context{DeployAllowed: ctx.DeployAllowed, MainlineAllowed: ctx.MainlineAllowed}, depth+1)
		}
	case "git":
		return checkGit(args, ctx)
	case "rm":
		return checkRm(args, ctx)
	case "systemctl":
		return checkSystemctl(args)
	case "service":
		if len(args) > 1 && stopVerbs[args[1]] {
			return Verdict{Rule: "service-stop", Why: "stops or restarts the " + args[0] + " service"}
		}
	case "shutdown", "reboot", "poweroff", "halt":
		return Verdict{Rule: "vm-stop", Why: "shuts down or restarts the machine"}
	case "pkill", "killall":
		return checkPkill(name, args)
	case "kill":
		return checkKill(args)
	case "cmdr", "commander":
		return checkCmdr(args)
	case "curl", "wget", "http":
		for _, a := range args {
			if chatControl.MatchString(a) {
				return Verdict{Rule: "chat-stop", Why: "stops, starts or replaces a Commander chat through its API"}
			}
		}
	case "just":
		for _, a := range args {
			if a == "deploy-prod" && !ctx.DeployAllowed {
				return deployVerdict()
			}
		}
	}
	// The deploy script however it is launched (python, uv run, poetry run…).
	if (strings.HasSuffix(name, "production_workflow.py") || hasSuffixArg(args, "production_workflow.py")) &&
		hasArg(args, "--confirm-production") && !ctx.DeployAllowed {
		return deployVerdict()
	}
	return Verdict{}
}

func deployVerdict() Verdict {
	return Verdict{Rule: "prod-deploy", Why: "deploys to production outside an approved release-deploy step"}
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func hasSuffixArg(args []string, suffix string) bool {
	for _, a := range args {
		if strings.HasSuffix(a, suffix) {
			return true
		}
	}
	return false
}

// mainlineBranches are the branches only a release's land-on-mainline step pushes to.
var mainlineBranches = map[string]bool{"main": true, "master": true}

// pushTarget is the branch a push refspec writes: the part after the colon, or
// the whole refspec when there is none.
func pushTarget(refspec string) string {
	if i := strings.LastIndexByte(refspec, ':'); i >= 0 {
		refspec = refspec[i+1:]
	}
	return strings.TrimPrefix(refspec, "refs/heads/")
}

// git push --force / -f / +refspec, push --delete / :ref, push to main or
// master outside a release's land-on-mainline step, branch -D.
func checkGit(args []string, ctx Context) Verdict {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		if (args[0] == "-C" || args[0] == "-c") && len(args) > 1 {
			args = args[1:]
		}
		args = args[1:]
	}
	if len(args) == 0 {
		return Verdict{}
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "push":
		positional := 0
		for _, a := range rest {
			switch {
			case a == "--force" || strings.HasPrefix(a, "--force-with-lease") || a == "--mirror":
				return Verdict{Rule: "force-push", Why: "force-pushes (" + a + ")"}
			case a == "--delete" || a == "--prune":
				return Verdict{Rule: "branch-delete", Why: "deletes remote branches (" + a + ")"}
			case shortFlag(a, 'f'):
				return Verdict{Rule: "force-push", Why: "force-pushes (" + a + ")"}
			case shortFlag(a, 'd'):
				return Verdict{Rule: "branch-delete", Why: "deletes a remote branch (" + a + ")"}
			case !strings.HasPrefix(a, "-"):
				positional++
				if positional > 1 && strings.HasPrefix(a, "+") {
					return Verdict{Rule: "force-push", Why: "force-pushes a +refspec (" + a + ")"}
				}
				if positional > 1 && strings.HasPrefix(a, ":") {
					return Verdict{Rule: "branch-delete", Why: "deletes a remote branch (" + a + ")"}
				}
				if positional > 1 && mainlineBranches[pushTarget(a)] && !ctx.MainlineAllowed {
					return Verdict{Rule: "mainline-push", Why: "pushes to " + pushTarget(a) + " outside a release's land-on-mainline step (an app's mainline changes only through its release process)"}
				}
			}
		}
	case "branch":
		del, force := false, false
		for _, a := range rest {
			switch {
			case a == "-D":
				return Verdict{Rule: "branch-delete", Why: "force-deletes a branch every worktree shares (-D)"}
			case a == "--delete" || shortFlag(a, 'd'):
				del = true
				force = force || shortFlag(a, 'f')
			case a == "--force" || shortFlag(a, 'f'):
				force = true
			}
		}
		if del && force {
			return Verdict{Rule: "branch-delete", Why: "force-deletes a branch every worktree shares"}
		}
	}
	return Verdict{}
}

// shortFlag: a is a cluster of single-letter flags (-fu) that includes c.
func shortFlag(a string, c byte) bool {
	if len(a) < 2 || a[0] != '-' || a[1] == '-' {
		return false
	}
	for i := 1; i < len(a); i++ {
		if !(a[i] >= 'a' && a[i] <= 'z' || a[i] >= 'A' && a[i] <= 'Z') {
			return false
		}
	}
	return strings.IndexByte(a[1:], c) >= 0
}

// rm -rf outside the agent's own worktree or a temporary directory.
func checkRm(args []string, ctx Context) Verdict {
	recursive, force := false, false
	var targets []string
	opts := true
	for _, a := range args {
		switch {
		case opts && a == "--":
			opts = false
		case opts && (a == "--recursive"):
			recursive = true
		case opts && a == "--force":
			force = true
		case opts && strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--"):
			recursive = recursive || strings.ContainsAny(a, "rR")
			force = force || strings.Contains(a, "f")
		case opts && strings.HasPrefix(a, "--"):
		default:
			targets = append(targets, a)
		}
	}
	if !recursive || !force {
		return Verdict{}
	}
	for _, t := range targets {
		if !safeTarget(t, ctx.Worktree) {
			return Verdict{Rule: "rm-rf", Why: "recursively force-deletes " + t + ", outside your own worktree or a temporary directory"}
		}
	}
	return Verdict{}
}

var tmpPrefixes = []string{"/tmp/", "/var/tmp/", "/dev/shm/", "$TMPDIR/", "${TMPDIR}/", "$TMPDIR", "${TMPDIR}"}

func safeTarget(t, worktree string) bool {
	t = strings.Trim(t, `"'`)
	for _, p := range tmpPrefixes {
		if strings.HasPrefix(t, p) && !strings.Contains(t, "..") && len(t) > len("/tmp/") {
			return true
		}
	}
	if t == "" || t == "/" || t == "." || t == "*" || t == ".." || strings.Contains(t, "..") ||
		strings.HasPrefix(t, "~") || strings.HasPrefix(t, "$") || strings.HasPrefix(t, "${") {
		return false
	}
	if worktree == "" || worktree == "/" {
		return false
	}
	if filepath.IsAbs(t) {
		clean, wt := filepath.Clean(t), filepath.Clean(worktree)
		return strings.HasPrefix(clean, wt+string(filepath.Separator))
	}
	return !strings.Contains(t, "$") // relative: inside the worktree
}

var stopVerbs = map[string]bool{"stop": true, "restart": true, "kill": true, "disable": true, "mask": true,
	"reload-or-restart": true, "try-restart": true, "isolate": true, "poweroff": true, "reboot": true, "halt": true, "kexec": true}

func checkSystemctl(args []string) Verdict {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if stopVerbs[a] {
			return Verdict{Rule: "service-stop", Why: "stops or restarts systemd units (systemctl " + a + ")"}
		}
		return Verdict{} // the first non-flag word is the verb
	}
	return Verdict{}
}

var protectedProcs = regexp.MustCompile(`(?i)cmdrd|commander|claude|postgres|uvicorn|temporal`)

func checkPkill(name string, args []string) Verdict {
	for _, a := range args {
		if a == "-g" || a == "--pgroup" || a == "-s" || a == "--session" {
			return Verdict{Rule: "kill-group", Why: "kills a process group or session (" + name + " " + a + ")"}
		}
	}
	for _, a := range args {
		if !strings.HasPrefix(a, "-") && protectedProcs.MatchString(a) {
			return Verdict{Rule: "service-stop", Why: "kills " + a + " processes (" + name + ")"}
		}
	}
	return Verdict{}
}

var negNum = regexp.MustCompile(`^-[0-9]+$`)

// kill of a process group: a negative pid after the signal, after --, or 0 / -1.
func checkKill(args []string) Verdict {
	sigSeen, dashdash := false, false
	for i, a := range args {
		switch {
		case a == "--":
			dashdash = true
		case dashdash && strings.HasPrefix(a, "-"):
			return groupKill(a)
		case a == "-s" || a == "-n":
			sigSeen = true
		case i == 0 && strings.HasPrefix(a, "-"):
			sigSeen = true // the signal (-9, -TERM)
		case negNum.MatchString(a) && sigSeen:
			return groupKill(a)
		case a == "0" || a == "-1":
			return groupKill(a)
		case !strings.HasPrefix(a, "-"):
			sigSeen = true
		}
	}
	return Verdict{}
}

func groupKill(a string) Verdict {
	return Verdict{Rule: "kill-group", Why: "kills a whole process group (" + a + "); kill explicit PIDs only"}
}

func checkCmdr(args []string) Verdict {
	if len(args) == 0 {
		return Verdict{}
	}
	switch args[0] {
	case "restart", "reload":
		return Verdict{Rule: "service-stop", Why: "restarts Commander (cmdr " + args[0] + ")"}
	case "install":
		for _, a := range args[1:] {
			if strings.HasPrefix(a, "--restart") {
				return Verdict{Rule: "service-stop", Why: "restarts cmdrd (cmdr install " + a + ")"}
			}
		}
	}
	return Verdict{}
}

var chatControl = regexp.MustCompile(`/api/chat/[^/\s]+/(stop|start|fresh|handover|popout)\b`)

// sshCommand is the remote command of an ssh call, or "".
func sshCommand(args []string) string {
	takesValue := "bcDEeFIiJLlmOoPpQRSWw"
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		a := args[i]
		if len(a) == 2 && strings.IndexByte(takesValue, a[1]) >= 0 {
			i++
		}
		i++
	}
	if i >= len(args)-1 {
		return ""
	}
	return strings.Join(args[i+1:], " ")
}

// remoteVM: ssh to exe.dev, whose commands manage the VMs themselves.
func remoteVM(args []string) bool {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			host := a
			if at := strings.LastIndexByte(host, '@'); at >= 0 {
				host = host[at+1:]
			}
			return host == "exe.dev"
		}
	}
	return false
}

var vmVerbs = regexp.MustCompile(`(?i)^\s*(\S+\s+)?(rm|stop|restart|delete|destroy|reboot|shutdown)\b`)

// devVMRestart: restarting (only restarting) a dev VM is allowed (Eric, Nov 18):
// a hung dev box blocks agents and restart loses no prod state.
var devVMRestart = regexp.MustCompile(`(?i)^\s*restart\s+[a-z0-9-]+-dev[0-9]*\s*$`)

func vmVerb(rest string) Verdict {
	if devVMRestart.MatchString(rest) {
		return Verdict{}
	}
	if vmVerbs.MatchString(rest) {
		return Verdict{Rule: "vm-stop", Why: "stops, restarts or removes a VM through exe.dev"}
	}
	return Verdict{}
}

var (
	sqlClient  = regexp.MustCompile(`(?i)(^|[\s;&|(/])(psql|mysql|sqlite3|clickhouse-client|clickhouse)\b`)
	sqlDrop    = regexp.MustCompile(`(?i)\b(drop\s+(table|database|schema|view|index|materialized\s+view)|truncate(\s+table)?)\b`)
	sqlDelete  = regexp.MustCompile(`(?i)\bdelete\s+from\b([^;'"]*)`)
	sqlWhere   = regexp.MustCompile(`(?i)\bwhere\b`)
	testTarget = regexp.MustCompile(`(?i)test|trial|scratch`)
)

// checkSQL refuses DROP/TRUNCATE, and DELETE without WHERE, sent through a
// database client to a database that is not a test one.
func checkSQL(command string) Verdict {
	if !sqlClient.MatchString(command) || testTarget.MatchString(command) {
		return Verdict{}
	}
	if m := sqlDrop.FindString(command); m != "" {
		return Verdict{Rule: "destructive-sql", Why: "runs " + strings.ToUpper(strings.Join(strings.Fields(m), " ")) + " against a non-test database"}
	}
	for _, m := range sqlDelete.FindAllStringSubmatch(command, -1) {
		if !sqlWhere.MatchString(m[1]) {
			return Verdict{Rule: "destructive-sql", Why: "runs DELETE without WHERE against a non-test database"}
		}
	}
	return Verdict{}
}

func base(w string) string { return filepath.Base(w) }

// HookOutput is the Claude Code PreToolUse JSON that refuses the call with v's message.
func HookOutput(v Verdict) []byte {
	b, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":            "PreToolUse",
		"permissionDecision":       "deny",
		"permissionDecisionReason": v.Message(),
	}})
	return b
}

// ReleaseDeployLabel marks an approved release-deploy step: its Operator may
// run the production deploy command.
const ReleaseDeployLabel = "cmdr:release-deploy"

// ReleaseLandLabel marks a release's land-on-mainline step: its Operator may
// push the release to the app's main or master branch.
const ReleaseLandLabel = "cmdr:release-land"
