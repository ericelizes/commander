package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// Local contains installation-specific integration settings, never credentials.
// It lives beside config.toml, outside the repository, and is read on demand.
type Local struct {
	Integrations struct {
		Linear struct {
			Workspace string `toml:"workspace"`
			// Team preserves the single-string setting for legacy title-prefix consumers.
			Team *string `toml:"-"`
			// Teams is the normalized, deduplicated union of team and teams.
			Teams     []string `toml:"teams"`
			TeamValue any      `toml:"team"`
		} `toml:"linear"`
	} `toml:"integrations"`
}

var workspaceSlug = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// LoadLocal reads local.toml beside the supplied config path. A missing file
// disables optional integrations; malformed files and unknown keys are errors.
func LoadLocal(configPath string) (*Local, error) {
	local := &Local{}
	path := filepath.Join(filepath.Dir(configPath), "local.toml")
	meta, err := toml.DecodeFile(path, local)
	if errors.Is(err, os.ErrNotExist) {
		return local, nil
	}
	if err != nil {
		return nil, fmt.Errorf("parse local config %s: %w", path, err)
	}
	if keys := meta.Undecoded(); len(keys) > 0 {
		return nil, unknownKeysError(keys)
	}
	switch v := local.Integrations.Linear.TeamValue.(type) {
	case nil:
	case string:
		local.Integrations.Linear.Team = &v
		local.Integrations.Linear.Teams = append(local.Integrations.Linear.Teams, v)
	case []any:
		for _, x := range v {
			t, ok := x.(string)
			if !ok {
				return nil, fmt.Errorf("linear.team must contain strings")
			}
			local.Integrations.Linear.Teams = append(local.Integrations.Linear.Teams, t)
		}
	default:
		return nil, fmt.Errorf("linear.team must be a string or list of strings")
	}
	teams := []string{}
	seen := map[string]bool{}
	for _, t := range local.Integrations.Linear.Teams {
		t = strings.ToUpper(strings.TrimSpace(t))
		if t != "" && !seen[t] {
			teams = append(teams, t)
			seen[t] = true
		}
	}
	local.Integrations.Linear.Teams = teams
	if ws := local.Integrations.Linear.Workspace; ws != "" && !workspaceSlug.MatchString(ws) {
		return nil, fmt.Errorf("local config: integrations.linear.workspace must be a workspace slug")
	}
	return local, nil
}
