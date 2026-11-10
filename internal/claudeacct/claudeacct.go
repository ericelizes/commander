// Package claudeacct manages Claude Code accounts: named config dirs that
// Claude Code reads through CLAUDE_CONFIG_DIR. "default" is the user's normal
// ~/.claude (no override); every other account lives under
// $COMMANDER_HOME/claude-accounts/<name>.
//
// Credentials stay per account because Claude Code keys them by config dir: on
// macOS the Keychain item "Claude Code-credentials" (default) or
// "Claude Code-credentials-<first 8 hex of sha256(dir)>" (CLAUDE_CONFIG_DIR
// set), account $USER; and a plaintext <dir>/.credentials.json fallback. So an
// account dir shares the user's settings, CLAUDE.md, commands, skills, agents
// and plugins by symlink, and copies the user-scope MCP servers into its own
// .claude.json, but never links .credentials.json or .claude.json.
package claudeacct

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Default is the user's normal Claude login (~/.claude).
const Default = "default"

// Reserved is the name the Codex account goes by in the account routes and
// lists, so no Claude account may take it.
const Reserved = "codex"

// Account is one Claude Code login.
type Account struct {
	Name        string `json:"name"`
	Dir         string `json:"dir"` // the config dir ("" never: default reports ~/.claude)
	LoggedIn    bool   `json:"logged_in"`
	Email       string `json:"email,omitempty"`
	AccountUUID string `json:"account_uuid,omitempty"`
	OrgUUID     string `json:"org_uuid,omitempty"`
	DuplicateOf string `json:"duplicate_of,omitempty"`
}

// Shared are the ~/.claude entries an account dir links to: everything that
// configures Claude, nothing that holds a login, history or session state.
var Shared = []string{"settings.json", "settings.local.json", "CLAUDE.md", "commands", "skills", "agents",
	"plugins", "hooks", "output-styles", "statusline.sh", "keybindings.json"}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// ValidName reports whether name can name an account (and a directory).
func ValidName(name string) bool { return nameRe.MatchString(name) }

// Root is where named accounts live.
func Root(home string) string { return filepath.Join(home, "claude-accounts") }

// Dir is the CLAUDE_CONFIG_DIR of account name; "" for default (no override).
func Dir(home, name string) string {
	if name == "" || name == Default {
		return ""
	}
	return filepath.Join(Root(home), name)
}

// DefaultDir is always ~/.claude, independent of the caller’s profile.
func DefaultDir() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".claude")
}

// ConfigDir is the config dir Claude uses for account name.
func ConfigDir(home, name string) string {
	if d := Dir(home, name); d != "" {
		return d
	}
	return DefaultDir()
}

// ProjectsDir is where account name keeps session transcripts.
func ProjectsDir(home, name string) string { return filepath.Join(ConfigDir(home, name), "projects") }

// RoleFile holds the account a role runs on; missing = default.
func RoleFile(home, role string) string { return filepath.Join(home, "chat", role+".account") }

// ForRole is role's account: the role file's name when that account exists,
// else Default.
func ForRole(home, role string) string {
	raw, err := os.ReadFile(RoleFile(home, role))
	if name := strings.TrimSpace(string(raw)); err == nil && Exists(home, name) {
		return name
	}
	return Default
}

// Env is the environment entries that put a claude child on account name:
// CLAUDE_CONFIG_DIR for a named account, nothing for default.
func Env(home, name string) []string {
	if d := Dir(home, name); d != "" {
		return []string{"CLAUDE_CONFIG_DIR=" + d}
	}
	return nil
}

// LoginCommand is the one line the user runs to log account name in.
func LoginCommand(home, name string, email ...string) string {
	suffix := ""
	if len(email) > 0 && strings.TrimSpace(email[0]) != "" {
		suffix = " --email " + shellQuote(strings.TrimSpace(email[0]))
	}
	if d := Dir(home, name); d != "" {
		return "CLAUDE_CONFIG_DIR=" + shellQuote(d) + " claude auth login" + suffix
	}
	return "env -u CLAUDE_CONFIG_DIR claude auth login" + suffix
}

// ValidLoginEmail reports whether email can be passed to `claude auth login`:
// no control characters (they end or rewrite the terminal line) and no leading
// dash (it would read as another flag). Empty means no hint and is fine.
func ValidLoginEmail(email string) bool {
	for _, r := range email {
		if unicode.IsControl(r) {
			return false
		}
	}
	return !strings.HasPrefix(strings.TrimSpace(email), "-")
}

// KeychainService is the macOS Keychain service Claude Code stores the OAuth
// credentials of configDir under ("" = no CLAUDE_CONFIG_DIR). The suffix hashes
// the CLAUDE_CONFIG_DIR string exactly as given, so it must match Env.
func KeychainService(configDir string) string {
	const base = "Claude Code-credentials"
	if configDir == "" {
		return base
	}
	sum := sha256.Sum256([]byte(configDir))
	return base + "-" + hex.EncodeToString(sum[:])[:8]
}

// Exists reports whether account name exists (default always does).
func Exists(home, name string) bool {
	if name == Default {
		return true
	}
	if !ValidName(name) {
		return false
	}
	info, err := os.Stat(Dir(home, name))
	return err == nil && info.IsDir()
}

// List is every account, default first, then by name.
func List(home string) []Account {
	out := []Account{Get(home, Default)}
	entries, _ := os.ReadDir(Root(home))
	var names []string
	for _, e := range entries {
		if e.IsDir() && ValidName(e.Name()) && e.Name() != Default {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, Get(home, n))
	}
	for i := range out {
		for j := 0; j < i; j++ {
			if SameIdentity(out[i], out[j]) {
				out[i].DuplicateOf = out[j].Name
				break
			}
		}
	}
	return out
}

// SameIdentity compares logged-in profiles, preferring the stable account UUID.
func SameIdentity(a, b Account) bool {
	if !a.LoggedIn || !b.LoggedIn {
		return false
	}
	if a.AccountUUID != "" && b.AccountUUID != "" {
		return a.AccountUUID == b.AccountUUID
	}
	return a.Email != "" && strings.EqualFold(strings.TrimSpace(a.Email), strings.TrimSpace(b.Email))
}

// Get describes account name (which need not exist: LoggedIn is then false).
func Get(home, name string) Account {
	a := Account{Name: name, Dir: ConfigDir(home, name)}
	if !Exists(home, name) {
		return a
	}
	override := Dir(home, name)
	a.LoggedIn = loggedIn(a.Dir, override)
	a.Email, a.AccountUUID, a.OrgUUID = identity(globalConfig(home, name))
	return a
}

// Create makes account name: its dir with the shared links and MCP servers.
// It does not log in: the user runs LoginCommand.
func Create(home, name string) (Account, error) {
	if name == Default {
		return Account{}, errors.New("default is the normal ~/.claude login and always exists")
	}
	if name == Reserved {
		return Account{}, errors.New("codex names the Codex account (sign-in routes are keyed by name); pick another name")
	}
	if !ValidName(name) {
		return Account{}, fmt.Errorf("invalid account name %q (a-z, 0-9, - and _, up to 32)", name)
	}
	dir := Dir(home, name)
	if _, err := os.Stat(dir); err == nil {
		return Account{}, fmt.Errorf("account %s already exists (%s)", name, dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Account{}, err
	}
	if err := Sync(home, name); err != nil {
		return Account{}, err
	}
	return Get(home, name), nil
}

// Sync brings account name up to date with the default account: links for
// Shared entries that appeared since, and the user-scope MCP servers copied
// into the account's .claude.json (rewritten only when they differ).
func Sync(home, name string) error {
	dir := Dir(home, name)
	if dir == "" {
		return nil
	}
	src := DefaultDir()
	for _, e := range Shared {
		target := filepath.Join(src, e)
		if _, err := os.Stat(target); err != nil {
			continue
		}
		link := filepath.Join(dir, e)
		if _, err := os.Lstat(link); err == nil {
			continue // already linked, or the account's own copy
		}
		if err := os.Symlink(target, link); err != nil {
			return err
		}
	}
	return syncMCP(globalConfig(home, Default), filepath.Join(dir, ".claude.json"))
}

// globalConfig is Claude's global config file for account name: ~/.claude.json
// for default without CLAUDE_CONFIG_DIR, else <config dir>/.claude.json.
func globalConfig(home, name string) string {
	if name == Default || name == "" {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, ".claude.json")
	}
	return filepath.Join(ConfigDir(home, name), ".claude.json")
}

func syncMCP(from, to string) error {
	var src struct {
		MCP json.RawMessage `json:"mcpServers"`
	}
	raw, err := os.ReadFile(from)
	if err != nil || json.Unmarshal(raw, &src) != nil || len(src.MCP) == 0 {
		return nil // no user-scope MCP servers to share
	}
	dst := map[string]json.RawMessage{}
	if raw, err := os.ReadFile(to); err == nil {
		if err := json.Unmarshal(raw, &dst); err != nil {
			return fmt.Errorf("%s: %w", to, err)
		}
	}
	if string(dst["mcpServers"]) == string(src.MCP) {
		return nil
	}
	dst["mcpServers"] = src.MCP
	out, err := json.MarshalIndent(dst, "", "  ")
	if err != nil {
		return err
	}
	tmp := to + ".cmdr-tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, to)
}

func identity(path string) (string, string, string) {
	var c struct {
		OAuth struct {
			Email       string `json:"emailAddress"`
			AccountUUID string `json:"accountUuid"`
			OrgUUID     string `json:"organizationUuid"`
		} `json:"oauthAccount"`
	}
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &c) != nil {
		return "", "", ""
	}
	return c.OAuth.Email, c.OAuth.AccountUUID, c.OAuth.OrgUUID
}

// KeychainHas reports whether the login Keychain holds service for the user.
// It reads attributes only (no -w), so it never touches the secret or prompts.
// Tests replace it.
var KeychainHas = func(service string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	return exec.Command("security", "find-generic-password", "-a", keychainUser(), "-s", service).Run() == nil
}

// keychainUser is the Keychain account Claude Code uses: $USER, else the OS user.
func keychainUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "claude-code-user"
}

var probeCache struct {
	sync.Mutex
	at map[string]time.Time
	ok map[string]bool
}

// probeTTL bounds how often the Keychain is asked about one dir (the app polls).
const probeTTL = 5 * time.Second

// loggedIn reports whether dir has credentials: the plaintext file, or the
// Keychain item for override (the CLAUDE_CONFIG_DIR value, "" = none).
func loggedIn(dir, override string) bool {
	// The file also holds MCP OAuth credentials after Claude signs out.
	// Those are not a Claude account and cannot supply plan usage.
	if raw, err := os.ReadFile(filepath.Join(dir, ".credentials.json")); err == nil {
		var credentials struct {
			OAuth struct {
				AccessToken string `json:"accessToken"`
			} `json:"claudeAiOauth"`
		}
		if json.Unmarshal(raw, &credentials) == nil && credentials.OAuth.AccessToken != "" {
			return true
		}
	}
	svc := KeychainService(override)
	probeCache.Lock()
	defer probeCache.Unlock()
	if probeCache.at == nil {
		probeCache.at, probeCache.ok = map[string]time.Time{}, map[string]bool{}
	}
	if at, ok := probeCache.at[svc]; ok && time.Since(at) < probeTTL {
		return probeCache.ok[svc]
	}
	ok := KeychainHas(svc)
	probeCache.at[svc], probeCache.ok[svc] = time.Now(), ok
	return ok
}

// ForgetProbes drops cached login checks (after a login, and in tests).
func ForgetProbes() {
	probeCache.Lock()
	probeCache.at, probeCache.ok = nil, nil
	probeCache.Unlock()
}

// unquotedRe is what a shell reads literally; anything else gets quoted.
var unquotedRe = regexp.MustCompile(`^[A-Za-z0-9@%+=:,./_-]+$`)

func shellQuote(s string) string {
	if unquotedRe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
