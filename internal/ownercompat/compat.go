// Package ownercompat reads owner vocabulary written by older releases.
// New records always use the neutral values returned by Normalize.
package ownercompat

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// legacyOwner is a synthetic retired alias used by compatibility fixtures.
// Historical personal aliases are intentionally absent from the publishable
// source: an installation names its own in Aliases' sources.
const legacyOwner = "sample-owner"

const LegacyNeedsLabel = "needs-" + legacyOwner

// AliasesEnv lists extra retired owner names (comma separated). It is read
// only by a process with no agent role: an agent (COMMANDER_ROLE or
// COMMANDER_MANAGER_ROLE set) could otherwise name any approver on its own
// command line and have it count as the user.
const AliasesEnv = "COMMANDER_LEGACY_OWNER_ALIASES"

// AliasesFile is read from $COMMANDER_HOME (else ~/.commander): one retired
// owner name per line, blank lines and # comments ignored. Agents run as the
// same OS user and could edit it: the agent guards refuse commands naming it,
// a speed bump only.
const AliasesFile = "legacy-owner-aliases"

// statEvery bounds how often the alias file is stat'ed: Normalize runs per
// label per bead on every snapshot load, so it must not touch the disk per call.
var statEvery = time.Second

type aliasSet struct {
	env, home string    // inputs this set was built from
	file      string    // file identity (size, mtime) when read
	checked   time.Time // last stat
	set       map[string]bool
}

var (
	current atomic.Pointer[aliasSet]
	loadMu  sync.Mutex
)

// agentRole reports a process started for an agent.
func agentRole() bool {
	return os.Getenv("COMMANDER_ROLE") != "" || os.Getenv("COMMANDER_MANAGER_ROLE") != ""
}

// Aliases returns every retired owner name that counts as the user: the
// built-in synthetic one plus those named in AliasesFile and, outside agent
// processes, AliasesEnv. The file is stat'ed at most once per statEvery and
// re-read only when its size or mtime changes. The map is shared: read only.
func Aliases() map[string]bool {
	env := ""
	if !agentRole() {
		env = os.Getenv(AliasesEnv)
	}
	dir := home()
	if c := current.Load(); c != nil && c.env == env && c.home == dir && time.Since(c.checked) < statEvery {
		return c.set
	}
	loadMu.Lock()
	defer loadMu.Unlock()
	c := current.Load()
	if c != nil && c.env == env && c.home == dir && time.Since(c.checked) < statEvery {
		return c.set
	}
	path := filepath.Join(dir, AliasesFile)
	file := ""
	st, err := os.Stat(path)
	if err == nil && !st.IsDir() {
		file = strconv.FormatInt(st.Size(), 10) + "\x00" + strconv.FormatInt(st.ModTime().UnixNano(), 10)
	}
	if c != nil && c.env == env && c.home == dir && c.file == file {
		next := *c
		next.checked = time.Now()
		current.Store(&next)
		return c.set
	}
	var data []byte
	if file != "" {
		data, _ = os.ReadFile(path)
	}
	set := map[string]bool{legacyOwner: true}
	for _, s := range strings.Split(env, ",") {
		addAlias(set, s)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		addAlias(set, line)
	}
	current.Store(&aliasSet{env: env, home: dir, file: file, checked: time.Now(), set: set})
	return set
}

// addAlias adds a plain name. "user" itself, prefixed values and the names of
// agent roles (commander, manager-*, operator-*) are never an alias.
func addAlias(set map[string]bool, s string) {
	s = strings.TrimSpace(s)
	if s == "" || s == "user" || strings.ContainsAny(s, ":, \t") {
		return
	}
	l := strings.ToLower(s)
	if l == "commander" || l == "manager" || l == "operator" || strings.HasPrefix(l, "manager-") || strings.HasPrefix(l, "operator-") {
		return
	}
	set[s] = true
}

func home() string {
	if h := os.Getenv("COMMANDER_HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".commander")
	}
	return ""
}

// NeedsLabels returns the legacy needs-<alias> labels in use, sorted.
func NeedsLabels() []string {
	out := []string{}
	for a := range Aliases() {
		out = append(out, "needs-"+a)
	}
	sort.Strings(out)
	return out
}

// IsLegacyNeeds reports a needs-<alias> label that reads as needs-user.
func IsLegacyNeeds(label string) bool {
	return label != "needs-user" && Normalize(label) == "needs-user"
}

func Normalize(value string) string {
	if value == "" {
		return value
	}
	a := Aliases()
	if a[value] {
		return "user"
	}
	if n, ok := strings.CutPrefix(value, "needs-"); ok && a[n] {
		return "needs-user"
	}
	return value
}
