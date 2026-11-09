package layout

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const opencodeThinkingVisibilityKey = "thinking_visibility"
const (
	opencodeAgentCycleKey        = "agent_cycle"
	opencodeAgentCycleReverseKey = "agent_cycle_reverse"
)

func ensureOpencodeCommanderPrefs() error {
	if err := ensureOpencodeThinkingHiddenByDefault(); err != nil {
		return err
	}
	if err := ensureOpencodeCommanderKeybinds(); err != nil {
		return err
	}
	return nil
}

func ensureOpencodeThinkingHiddenByDefault() error {
	path, err := opencodeKVPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir opencode state dir: %w", err)
	}

	prefs := map[string]any{}
	if body, err := os.ReadFile(path); err == nil {
		if len(body) > 0 {
			if err := json.Unmarshal(body, &prefs); err != nil {
				return fmt.Errorf("parse opencode kv: %w", err)
			}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read opencode kv: %w", err)
	}

	if _, exists := prefs[opencodeThinkingVisibilityKey]; exists {
		return nil
	}
	prefs[opencodeThinkingVisibilityKey] = false

	body, err := json.MarshalIndent(prefs, "", "  ")
	if err != nil {
		return fmt.Errorf("encode opencode kv: %w", err)
	}
	body = append(body, '\n')
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("write opencode kv: %w", err)
	}
	return nil
}

func ensureOpencodeCommanderKeybinds() error {
	path, err := opencodeTUIConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir opencode config dir: %w", err)
	}

	cfg := map[string]any{}
	if body, err := os.ReadFile(path); err == nil {
		if len(body) > 0 {
			if err := json.Unmarshal(body, &cfg); err != nil {
				return fmt.Errorf("parse opencode tui config: %w", err)
			}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read opencode tui config: %w", err)
	}

	if _, ok := cfg["$schema"]; !ok {
		cfg["$schema"] = "https://opencode.ai/tui.json"
	}

	keybinds, _ := cfg["keybinds"].(map[string]any)
	if keybinds == nil {
		keybinds = map[string]any{}
	}
	keybinds[opencodeAgentCycleKey] = "none"
	keybinds[opencodeAgentCycleReverseKey] = "none"
	cfg["keybinds"] = keybinds

	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode opencode tui config: %w", err)
	}
	body = append(body, '\n')
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("write opencode tui config: %w", err)
	}
	return nil
}

func opencodeKVPath() (string, error) {
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		return filepath.Join(stateHome, "opencode", "kv.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".local", "state", "opencode", "kv.json"), nil
}

func opencodeTUIConfigPath() (string, error) {
	if configHome := os.Getenv("XDG_CONFIG_HOME"); configHome != "" {
		return filepath.Join(configHome, "opencode", "tui.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".config", "opencode", "tui.json"), nil
}
