package managerguard

import (
	"strings"
	"testing"
)

var agentWrites = map[string]string{
	"bd create t --parent x-1":                       "create",
	"bd update x-1 --title t":                        "update",
	"bd close x-1":                                   "close",
	"bd comments add x-1 hi":                         "comments",
	"bd -C /repo comments add x-1 hi":                "comments",
	"bd --db /x/.beads/beads.db comments add x-1 hi": "comments",
	"bd label add x-1 a":                             "label",
	"bd label remove x-1 a":                          "label",
	"bd dep add a b":                                 "dep",
	"bd dep rm a b":                                  "dep",
	"bd defer x-1":                                   "defer",
	"bd delete x-1 --force":                          "delete",
	"bd set-state x-1 cmdr=done":                     "set-state",
	"bd import f.jsonl":                              "import",
	"bd sync":                                        "sync",
	"br update x-1 --status open":                    "update",
	"/opt/u/.local/bin/br create t":                  "create",
	"FOO=1 bd close x-1":                             "close",
	"env BEADS_DIR=/x bd close x-1":                  "close",
	"timeout 5 bd close x-1":                         "close",
	"cd /repo && bd close x-1":                       "close",
	"bash -c 'bd close x-1'":                         "close",
	"echo $(bd create t)":                            "create",
	"bd show x-1 | head && bd comments add x-1 hi":   "comments",
}

var agentReads = []string{
	"bd show x-1", "bd list --json", "bd ready", "bd comments x-1", "bd dep list x-1", "bd dep tree x-1",
	"bd label list x-1", "bd -C /repo list --json | head", "bd --db /x/.beads/beads.db show x-1 --json",
	"br show x-1", "bd sync --flush-only", "bd export -o /tmp/x.jsonl", "bd search foo", "bd",
	"echo bd create is refused", "grep 'bd close' notes.md", "cmdr internal-set-objective --project p --bead b --state blocked",
	"cmdr internal-ship op/x", "git commit -m 'bd create'",
}

func TestBeadsWriteVerbRefusesEveryWriteVerb(t *testing.T) {
	for cmd, want := range agentWrites {
		if got := BeadsWriteVerb(cmd); got != want {
			t.Errorf("%q: verb %q, want %q", cmd, got, want)
		}
	}
}

func TestBeadsWriteVerbAllowsReads(t *testing.T) {
	for _, cmd := range agentReads {
		if got := BeadsWriteVerb(cmd); got != "" {
			t.Errorf("%q: read refused as %q", cmd, got)
		}
	}
}

func TestManagerClassifyRefusesBeadsWritesAtProjectRoot(t *testing.T) {
	t.Setenv("COMMANDER_AGENT_WRITE_GUARD", "")
	for cmd := range agentWrites {
		v := Classify(cmd, "/repo")
		if !v.Deny {
			t.Errorf("%q allowed for a Manager at the project root", cmd)
		}
	}
	for _, cmd := range agentReads {
		if v := Classify(cmd, "/repo"); v.Deny && strings.HasPrefix(v.Why, BeadsWriteWhy) {
			t.Errorf("%q: read refused as a beads write", cmd)
		}
	}
}

func TestAgentWriteGuardKillSwitch(t *testing.T) {
	for _, off := range []string{"off", "OFF", "0", "false", "no"} {
		t.Setenv("COMMANDER_AGENT_WRITE_GUARD", off)
		if !AgentWriteGuardOff() {
			t.Errorf("%q: switch not off", off)
		}
		if v := Classify("bd close x-1", "/repo"); v.Deny && strings.HasPrefix(v.Why, BeadsWriteWhy) {
			t.Errorf("%q: still refused", off)
		}
	}
	t.Setenv("COMMANDER_AGENT_WRITE_GUARD", "on")
	if AgentWriteGuardOff() {
		t.Error("on read as off")
	}
}

func TestBeadsWriteReasonNamesTheCmdrVerb(t *testing.T) {
	for verb, want := range map[string]string{
		"create": "internal-create-objective", "close": "internal-close-objective", "set-state": "internal-phase",
		"comments": "internal-comment", "dep": "internal-dep", "update": "internal-set-objective", "label": "internal-set-objective",
		"import": "cmdr internal-",
	} {
		if r := BeadsWriteReason(verb); !strings.Contains(r, want) {
			t.Errorf("%s: reason lacks %q: %s", verb, want, r)
		}
	}
}

func TestBeadsWriteVerbGroupsXargsAndHelp(t *testing.T) {
	for cmd, want := range map[string]string{
		"(bd close x-1)":                    "close",
		"( bd close x-1 )":                  "close",
		"{ bd close x-1; }":                 "close",
		"echo x-1 | xargs bd close":         "close",
		"echo x-1 | xargs -I{} bd close {}": "close",
		"echo x | xargs -n1 -- br update":   "update",
	} {
		if got := BeadsWriteVerb(cmd); got != want {
			t.Errorf("%q: %q, want %q", cmd, got, want)
		}
	}
	for _, cmd := range []string{"bd update --help", "bd close -h", "bd update --help | head", "echo x | xargs bd show", "(bd show x-1)", "bd --version"} {
		if got := BeadsWriteVerb(cmd); got != "" {
			t.Errorf("%q refused as %q", cmd, got)
		}
	}
	if BeadsWriteVerb("bd update x-1 --help") == "" {
		t.Error("a real update with --help among values must stay refused")
	}
}

func TestNamesGuardBypass(t *testing.T) {
	t.Setenv("COMMANDER_AGENT_WRITE_GUARD", "")
	for _, cmd := range []string{"COMMANDER_BR_HELD=1 echo hi", "export COMMANDER_AGENT_WRITE_GUARD=off", "env -u COMMANDER_BR_HELD ls", "echo COMMANDER_BR_HELD"} {
		if !NamesGuardBypass(cmd) {
			t.Errorf("%q not flagged", cmd)
		}
		if v := Classify(cmd, ""); !v.Deny || !strings.Contains(v.Why, BypassVerb) {
			t.Errorf("manager allowed %q: %+v", cmd, v)
		}
	}
	if NamesGuardBypass("bd show x-1") {
		t.Error("false positive")
	}
	t.Setenv("COMMANDER_AGENT_WRITE_GUARD", "off")
	if v := Classify("echo COMMANDER_BR_HELD", ""); v.Deny && strings.Contains(v.Why, BypassVerb) {
		t.Error("kill switch ignored")
	}
}

func TestBeadsWriteVerbEval(t *testing.T) {
	for cmd, want := range map[string]string{
		`eval "bd close X"`:                   "close",
		`eval bd close X`:                     "close",
		`eval 'bd close X'`:                   "close",
		`eval "eval 'bd close X'"`:            "close",
		`eval "bd close 'X'"`:                 "close",
		`env FOO=1 eval "bd close X"`:         "close",
		`command eval "bd close X"`:           "close",
		`cd /r && eval "br update X"`:         "update",
		`{ eval "bd close X"; }`:              "close",
		`(eval bd close X)`:                   "close",
		`eval "echo hi; bd comments add X y"`: "comments",
	} {
		if got := BeadsWriteVerb(cmd); got != want {
			t.Errorf("%q: %q, want %q", cmd, got, want)
		}
	}
	// eval of a read is a read: allowed on purpose.
	for _, cmd := range []string{`eval "bd show X"`, `eval bd show X`, `eval 'bd list'`, `eval "echo hi"`, `eval "bd close --help"`} {
		if got := BeadsWriteVerb(cmd); got != "" {
			t.Errorf("%q refused as %q", cmd, got)
		}
	}
}

// M6/m4: a help, version or verbose flag before the verb does not hide it, and bd human's
// respond/dismiss (they close beads) are writes; a bare version or help call stays a read.
func TestBeadsWriteVerbFlagsBeforeTheVerbAndHuman(t *testing.T) {
	for _, c := range []string{"bd -v close X", "br -v close X", "bd --verbose close X", "bd --help close X", "bd --version update X --status open",
		"bd human respond X", "bd human dismiss X", "bd human"} {
		if BeadsWriteVerb(c) == "" {
			t.Errorf("%q passed the agent write guard", c)
		}
	}
	for _, c := range []string{"bd --version", "bd -v", "bd --help", "bd -v show X", "bd human list", "bd close --help"} {
		if v := BeadsWriteVerb(c); v != "" {
			t.Errorf("%q refused as %q", c, v)
		}
	}
}
