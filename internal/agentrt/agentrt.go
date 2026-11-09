// Package agentrt resolves which agent CLI (opencode or Claude Code) drives
// each commander tier, and owns the Claude Code invocation shapes.
//
// Resolution order: $COMMANDER_RUNTIME (all tiers), then the [runtime] table
// in ~/.commander/config.toml (per-tier value, then default), then opencode.
package agentrt

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/example-user/commander/internal/config"
)

// Kind names an agent CLI.
type Kind string

const (
	Opencode Kind = "opencode"
	Claude   Kind = "claude"
)

// Tier names a commander agent layer.
type Tier string

const (
	TierCommander Tier = "commander"
	TierLead      Tier = "lead"
	TierOperator  Tier = "operator"
)

// EnvVar overrides the runtime for every tier when set.
const EnvVar = "COMMANDER_RUNTIME"

// CodexShareEnvVar and CodexManagerShareEnvVar override [runtime]
// codex_operator_share and codex_manager_share.
const (
	CodexShareEnvVar        = "COMMANDER_CODEX_OPERATOR_SHARE"
	CodexManagerShareEnvVar = "COMMANDER_CODEX_MANAGER_SHARE"
)

// DefaultCodexOperatorShare and DefaultCodexManagerShare are the shares of
// Operators and Managers on Codex when nothing is configured: none (Claude
// is the default), so only a configured share draws it.
const (
	DefaultCodexOperatorShare = 0.0
	DefaultCodexManagerShare  = 0.0
)

// CodexOperatorShare is the fraction (0-1) of Operators spawned without an
// explicit runtime that run on Codex: $COMMANDER_CODEX_OPERATOR_SHARE, then
// [runtime] codex_operator_share, then DefaultCodexOperatorShare.
func CodexOperatorShare() float64 {
	return share(CodexShareEnvVar, func(r config.Runtime) *float64 { return r.CodexOperatorShare }, DefaultCodexOperatorShare)
}

// CodexManagerShare is the fraction (0-1) of Managers on Codex: what a new
// Manager draws against and what `cmdr runtime rebalance` aims for.
func CodexManagerShare() float64 {
	return share(CodexManagerShareEnvVar, func(r config.Runtime) *float64 { return r.CodexManagerShare }, DefaultCodexManagerShare)
}

// AgentRole names the agent this process runs for, "" for a person: chat roles
// and host Operators carry COMMANDER_ROLE; an Operator started before its
// launcher set that has only COMMANDER_MANAGER_ROLE (its Manager).
func AgentRole() string {
	if role := os.Getenv("COMMANDER_ROLE"); role != "" {
		return role
	}
	if mgr := os.Getenv("COMMANDER_MANAGER_ROLE"); mgr != "" {
		return "an Operator of " + mgr
	}
	return ""
}

// share reads a runtime share. The env override is a person's: in an agent's
// process (AgentRole) only config.toml counts, so an agent cannot draw Codex by
// setting the variable on its own command.
func share(env string, field func(config.Runtime) *float64, def float64) float64 {
	if v := os.Getenv(env); v != "" && AgentRole() == "" {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return min(max(f, 0), 1)
		}
	}
	if cfg := loadConfig(); cfg != nil && field(cfg.Runtime) != nil {
		return min(max(*field(cfg.Runtime), 0), 1)
	}
	return def
}

// For returns the runtime configured for tier.
func For(tier Tier) Kind {
	if v := os.Getenv(EnvVar); v != "" {
		return parseKind(v)
	}
	cfg := loadConfig()
	if cfg == nil {
		return Opencode
	}
	var v string
	switch tier {
	case TierCommander:
		v = cfg.Runtime.Commander
	case TierLead:
		v = cfg.Runtime.Lead
	case TierOperator:
		v = cfg.Runtime.Operator
	}
	if v == "" {
		v = cfg.Runtime.Default
	}
	return parseKind(v)
}

// Model returns the configured model for tier in opencode "provider/model"
// form, or "" when no config is readable.
func Model(tier Tier) string {
	cfg := loadConfig()
	if cfg == nil {
		cfg = config.Defaults()
	}
	switch tier {
	case TierCommander:
		return cfg.Models.DefaultCommander
	case TierLead:
		return cfg.Models.DefaultLead
	default:
		return cfg.Models.DefaultOperator
	}
}

// SessionFileName returns the session-id file name for base under kind.
// opencode keeps the historical "<base>.session-id"; Claude uses
// "<base>.claude.session-id" so switching runtimes never feeds one CLI the
// other's id and each keeps its own history.
func SessionFileName(base string, kind Kind) string {
	if kind == Claude {
		return base + ".claude.session-id"
	}
	return base + ".session-id"
}

func parseKind(v string) Kind {
	if strings.EqualFold(strings.TrimSpace(v), string(Claude)) {
		return Claude
	}
	return Opencode
}

// loadConfig reads the commander config, returning nil when it is missing or
// invalid. Missing is checked first so column panes don't spam config.Load's
// "using defaults" warning on every render.
func loadConfig() *config.Config {
	path := filepath.Join(commanderHome(), "config.toml")
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil
	}
	return cfg
}

// CommanderHome is $COMMANDER_HOME, else ~/.commander.
func CommanderHome() string { return commanderHome() }

func commanderHome() string {
	if home := os.Getenv("COMMANDER_HOME"); home != "" {
		return home
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".commander")
}
