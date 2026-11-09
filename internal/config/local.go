package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/BurntSushi/toml"
)

// Local contains installation-specific integration settings, never credentials.
// It lives beside config.toml, outside the repository, and is read on demand.
type Local struct {
	Integrations struct {
		Linear struct {
			Workspace string  `toml:"workspace"`
			Team      *string `toml:"team"`
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
	if ws := local.Integrations.Linear.Workspace; ws != "" && !workspaceSlug.MatchString(ws) {
		return nil, fmt.Errorf("local config: integrations.linear.workspace must be a workspace slug")
	}
	return local, nil
}
