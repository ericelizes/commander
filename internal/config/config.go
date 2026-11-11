// Package config owns commander's TOML configuration: the on-disk file at
// ~/.commander/config.toml, the in-memory Config struct, and the fsnotify-
// based hot reload pipeline.
//
// Two design decisions worth flagging:
//
//  1. Strict-mode parsing. A silent unknown-field policy hides typos until
//     something downstream misbehaves. We use BurntSushi/toml's MetaData.Undecoded() to detect
//     unknown keys and fail Load with an error that names every offender.
//
//  2. Missing file is non-fatal. A user running cmdr for the first time has
//     no config; we return Defaults() and write a warning to stderr. Only
//     a malformed/invalid file is fatal.
package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the in-memory representation of ~/.commander/config.toml.
type Config struct {
	// OperatorSandbox is "" (default: host Operators) or "docker" (opt-in).
	OperatorSandbox string             `toml:"operator_sandbox"`
	Concurrency     Concurrency        `toml:"concurrency"`
	Ship            Ship               `toml:"ship"`
	Ceilings        Ceilings           `toml:"ceilings"`
	Models          Models             `toml:"models"`
	Runtime         Runtime            `toml:"runtime"`
	API             API                `toml:"api"`
	User            User               `toml:"user"`
	Needs           Needs              `toml:"needs"`
	Projects        map[string]Project `toml:"projects"`
}

// API configures cmdrd's local HTTP API. Port 0 means the default (8787); a
// taken port falls through to the next free one (see $COMMANDER_HOME/api-url).
type API struct {
	Port int `toml:"port"`
}

// Concurrency bounds parallel work: Operators per workstream (a workstream's
// max-operators:<n> label overrides it) and build slots machine-wide.
//
// MaxOperatorsPerProject and MaxOperatorsGlobal are no longer enforced; they
// still parse so existing config files keep loading.
type Concurrency struct {
	ExecutionSlots int `toml:"execution_slots"` // generic execution capacity; zero uses eight local slots
	MaxOperatorsPerWorkstream int `toml:"max_operators_per_workstream"`
	// BuildSlots is how many full builds/suites run at once (internal/slot);
	// 0 = max(2, cores/2). $COMMANDER_BUILD_SLOTS overrides it.
	BuildSlots int `toml:"build_slots"`
	// BrPerDB and BrGlobal bound concurrent br processes per project db
	// (default 2) and overall (default 6), across processes
	// ($COMMANDER_BR_PER_DB / $COMMANDER_BR_GLOBAL override).
	BrPerDB                int `toml:"br_per_db"`
	BrGlobal               int `toml:"br_global"`
	MaxOperatorsPerProject int `toml:"max_operators_per_project"`
	MaxOperatorsGlobal     int `toml:"max_operators_global"`
}

// Needs tunes NEEDS YOU cards. All keys are optional (nil = the default);
// 0 switches a rule off. StaleDays: a card raised longer ago reads out of date
// (default 7). PushCount / PushDays: a wait or park that moves the same
// bead's date later is refused without --push-reason once it was pushed
// PushCount times, or when the target would slide more than PushDays from
// where it started (defaults 3 and 7).
type Needs struct {
	StaleDays *int `toml:"stale_days"`
	PushCount *int `toml:"push_count"`
	PushDays  *int `toml:"push_days"`
}

// Default values of the [needs] keys.
const (
	DefaultStaleDays = 7
	DefaultPushCount = 3
	DefaultPushDays  = 7
)

func orDefault(v *int, d int) int {
	if v == nil {
		return d
	}
	return *v
}

// StaleDaysOr is stale_days, or the default when unset (0 = off).
func (n Needs) StaleDaysOr() int { return orDefault(n.StaleDays, DefaultStaleDays) }

// PushCountOr is push_count, or the default when unset (0 = off).
func (n Needs) PushCountOr() int { return orDefault(n.PushCount, DefaultPushCount) }

// PushDaysOr is push_days, or the default when unset (0 = off).
func (n Needs) PushDaysOr() int { return orDefault(n.PushDays, DefaultPushDays) }

// Ship configures cmdr internal-ship. MaxThreads is the user thread count
// above which a verify waits before starting (0 = never wait; unset = 2400);
// $COMMANDER_SHIP_MAX_THREADS overrides it.
type Ship struct {
	MaxThreads *int `toml:"max_threads"`
}

// Ceilings are per-operator safety bounds (cost, tokens, time, retries).
type Ceilings struct {
	MaxUSDPerOperator    float64 `toml:"max_usd_per_operator"`
	MaxTokensPerOperator int     `toml:"max_tokens_per_operator"`
	MaxWallclockMinutes  int     `toml:"max_wallclock_minutes"`
	MaxRetries           int     `toml:"max_retries"`
}

// Models picks the default model identifier for each agent layer.
type Models struct {
	DefaultCommander string `toml:"default_commander"`
	DefaultLead      string `toml:"default_lead"`
	DefaultOperator  string `toml:"default_operator"`
}

// Runtime picks the agent CLI ("opencode" or "claude") that drives each tier.
// Default applies to every tier; a non-empty per-tier value overrides it.
type Runtime struct {
	Default   string `toml:"default"`
	Commander string `toml:"commander"`
	Lead      string `toml:"lead"`
	Operator  string `toml:"operator"`
	// CodexOperatorShare is the fraction of Operators spawned without an
	// explicit runtime that go to Codex (nil = the default, 0: none).
	CodexOperatorShare *float64 `toml:"codex_operator_share"`
	// CodexManagerShare is the fraction of Managers on Codex (nil = 0: none).
	CodexManagerShare *float64 `toml:"codex_manager_share"`
}

// Project is a per-project override (type + auto-spawn). MaxOperators is no
// longer enforced (caps are per workstream); it still parses.
//
// AutoSpawn (default false) opts the project into the U18 set-and-forget
// path: when true, the scheduler may spawn operators for ready Tasks without
// asking the user for confirmation, up to each workstream's Operator cap. When
// false (default), the scheduler emits ProposalEvents for the U17 confirm
// flow instead. Per-project so a high-trust project can be set-and-forget
// while higher-risk projects keep the human in the loop.
type Project struct {
	Type         string `toml:"type"`
	MaxOperators int    `toml:"max_operators"`
	AutoSpawn    bool   `toml:"auto_spawn"`
}

// User is who the app is for: [user] name is shown in the app
// (default "the operator"; see UserName).
type User struct {
	Name string `toml:"name"`
	// Timezone is the IANA zone (America/New_York) board times are shown in;
	// empty = the process zone. See Location.
	Timezone string `toml:"timezone"`
}

// Location is the zone for [user] timezone: time.Local when unset, and
// time.Local with an error when the name is not a known IANA zone.
func (u User) Location() (*time.Location, error) {
	name := strings.TrimSpace(u.Timezone)
	if name == "" {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.Local, fmt.Errorf("config: [user] timezone %q: %w", name, err)
	}
	return loc, nil
}

// knownTopLevelKeys is the small set of root-level table names we accept.
// Used by suggestUnknownKey to offer a typo correction.
var knownTopLevelKeys = []string{"operator_sandbox", "concurrency", "ship", "ceilings", "models", "runtime", "api", "user", "projects"}

// Load reads the TOML file at path and returns a fully populated *Config.
//
// Behaviour:
//   - File does not exist: returns Defaults(); writes a warning to stderr;
//     no error.
//   - File exists but unreadable / malformed / invalid types / unknown keys
//     / fails validation: returns nil, error.
//   - File exists and parses cleanly: missing sub-sections (e.g. omitted
//     [ceilings]) are filled from Defaults(); per-project entries keep their
//     user-supplied values.
//
// Strict-mode unknown-key detection happens via MetaData.Undecoded(): any key
// the user wrote that didn't bind to a struct field is reported with a typo
// suggestion when one is plausible.
func Load(path string) (*Config, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "commander: config file not found at %s; using defaults\n", path)
		return Defaults(), nil
	} else if err != nil {
		return nil, fmt.Errorf("stat config %s: %w", path, err)
	}

	cfg := &Config{}
	meta, err := toml.DecodeFile(path, cfg)
	if err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	if undec := meta.Undecoded(); len(undec) > 0 {
		return nil, unknownKeysError(undec)
	}

	applyDefaults(cfg)

	if err := Validate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyDefaults fills any zero-valued sub-section fields with the defaults.
// Sub-sections the user omitted decode to zero-value structs, which we treat
// as "use defaults". We do NOT overlay defaults onto fields the user set
// explicitly to a non-zero value.
func applyDefaults(cfg *Config) {
	d := Defaults()

	if cfg.Concurrency.MaxOperatorsPerWorkstream == 0 {
		cfg.Concurrency.MaxOperatorsPerWorkstream = d.Concurrency.MaxOperatorsPerWorkstream
	}

	if cfg.Ceilings.MaxUSDPerOperator == 0 {
		cfg.Ceilings.MaxUSDPerOperator = d.Ceilings.MaxUSDPerOperator
	}
	if cfg.Ceilings.MaxTokensPerOperator == 0 {
		cfg.Ceilings.MaxTokensPerOperator = d.Ceilings.MaxTokensPerOperator
	}
	if cfg.Ceilings.MaxWallclockMinutes == 0 {
		cfg.Ceilings.MaxWallclockMinutes = d.Ceilings.MaxWallclockMinutes
	}
	if cfg.Ceilings.MaxRetries == 0 {
		cfg.Ceilings.MaxRetries = d.Ceilings.MaxRetries
	}

	if cfg.Models.DefaultCommander == "" {
		cfg.Models.DefaultCommander = d.Models.DefaultCommander
	}
	if cfg.Models.DefaultLead == "" {
		cfg.Models.DefaultLead = d.Models.DefaultLead
	}
	if cfg.Models.DefaultOperator == "" {
		cfg.Models.DefaultOperator = d.Models.DefaultOperator
	}

	if cfg.Runtime.Default == "" {
		cfg.Runtime.Default = d.Runtime.Default
	}

	if cfg.Projects == nil {
		cfg.Projects = map[string]Project{}
	}
}

// Validate checks domain rules that TOML parsing alone cannot enforce:
// project type must be "code" or "knowledge"; numeric caps must be positive.
func Validate(cfg *Config) error {
	if cfg.Concurrency.MaxOperatorsPerWorkstream <= 0 {
		return fmt.Errorf("config: concurrency.max_operators_per_workstream must be > 0")
	}
	if cfg.Concurrency.BuildSlots < 0 {
		return fmt.Errorf("config: concurrency.build_slots must be >= 0")
	}
	for k, v := range map[string]*int{"stale_days": cfg.Needs.StaleDays, "push_count": cfg.Needs.PushCount, "push_days": cfg.Needs.PushDays} {
		if v != nil && *v < 0 {
			return fmt.Errorf("config: needs.%s must be >= 0", k)
		}
	}
	if m := cfg.Ship.MaxThreads; m != nil && *m < 0 {
		return fmt.Errorf("config: ship.max_threads must be >= 0")
	}
	for k, v := range map[string]*float64{"codex_manager_share": cfg.Runtime.CodexManagerShare, "codex_operator_share": cfg.Runtime.CodexOperatorShare} {
		if v != nil && (*v < 0 || *v > 1) {
			return fmt.Errorf("config: runtime.%s must be between 0 and 1", k)
		}
	}
	if cfg.Ceilings.MaxUSDPerOperator <= 0 {
		return fmt.Errorf("config: ceilings.max_usd_per_operator must be > 0")
	}
	if cfg.Ceilings.MaxTokensPerOperator <= 0 {
		return fmt.Errorf("config: ceilings.max_tokens_per_operator must be > 0")
	}
	if cfg.Ceilings.MaxWallclockMinutes <= 0 {
		return fmt.Errorf("config: ceilings.max_wallclock_minutes must be > 0")
	}
	if cfg.Ceilings.MaxRetries < 0 {
		return fmt.Errorf("config: ceilings.max_retries must be >= 0")
	}

	for key, v := range map[string]string{
		"default":   cfg.Runtime.Default,
		"commander": cfg.Runtime.Commander,
		"lead":      cfg.Runtime.Lead,
		"operator":  cfg.Runtime.Operator,
	} {
		switch v {
		case "", "opencode", "claude":
		default:
			return fmt.Errorf("config: runtime.%s = %q invalid; must be \"opencode\" or \"claude\"", key, v)
		}
	}

	for key, s := range map[string]*float64{"codex_operator_share": cfg.Runtime.CodexOperatorShare, "codex_manager_share": cfg.Runtime.CodexManagerShare} {
		if s != nil && (*s < 0 || *s > 1) {
			return fmt.Errorf("config: runtime.%s = %v invalid; must be 0-1", key, *s)
		}
	}

	if cfg.API.Port < 0 || cfg.API.Port > 65535 {
		return fmt.Errorf("config: api.port = %d invalid; must be 0-65535", cfg.API.Port)
	}

	for name, p := range cfg.Projects {
		switch p.Type {
		case "code", "knowledge":
		case "":
			return fmt.Errorf("config: projects.%s.type is required (\"code\" or \"knowledge\")", name)
		default:
			return fmt.Errorf("config: projects.%s.type = %q invalid; must be \"code\" or \"knowledge\"", name, p.Type)
		}
	}
	return nil
}

// unknownKeysError formats Undecoded() keys into a single error that names
// every offender and offers a typo suggestion when one is obvious. Keys are
// sorted for stable error output (and stable tests).
func unknownKeysError(keys []toml.Key) error {
	strs := make([]string, 0, len(keys))
	for _, k := range keys {
		strs = append(strs, k.String())
	}
	sort.Strings(strs)

	var b strings.Builder
	for i, k := range strs {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString("unknown config key: ")
		b.WriteString(k)
		if suggestion := suggestUnknownKey(k); suggestion != "" {
			b.WriteString(" (typo? did you mean ")
			b.WriteString(suggestion)
			b.WriteString("?)")
		}
	}
	return errors.New(b.String())
}

// suggestUnknownKey returns a best-guess correct key, or "" if nothing
// plausible is close. It compares the leaf field name against the union of
// known top-level table names and the toml-tagged fields of every config
// sub-struct.
func suggestUnknownKey(bad string) string {
	parts := strings.Split(bad, ".")
	leaf := parts[len(parts)-1]

	candidates := append([]string{}, knownTopLevelKeys...)
	candidates = append(candidates,
		"max_operators_per_workstream", "build_slots", "max_threads",
		"max_operators_per_project", "max_operators_global",
		"max_usd_per_operator", "max_tokens_per_operator",
		"max_wallclock_minutes", "max_retries",
		"default_commander", "default_lead", "default_operator",
		"default", "commander", "lead", "operator",
		"type", "max_operators", "auto_spawn",
	)

	best := ""
	bestDist := 1<<31 - 1
	for _, c := range candidates {
		d := levenshtein(leaf, c)
		if d < bestDist {
			bestDist = d
			best = c
		}
	}
	// Only suggest if the edit distance is small relative to the key length.
	if bestDist > len(leaf)/2+1 {
		return ""
	}
	// Reattach the table prefix for sub-section keys.
	if len(parts) > 1 {
		return strings.Join(parts[:len(parts)-1], ".") + "." + best
	}
	return best
}

// levenshtein computes the edit distance between a and b. Plain DP.
func levenshtein(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}
