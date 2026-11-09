package config

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"strings"
)

// SetValue writes key = value (value already TOML-encoded, e.g. "0.25" or
// `"Ada"`) into [table] of the file at path, keeping every other line and
// comment as it is: it replaces the key's line in that table, or adds it at the
// table's end, or adds the table at the end of the file. The result must load.
func SetValue(path, table, key, value string) error {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(raw) == 0 {
		lines = nil
	}
	header := regexp.MustCompile(`^\s*\[([^\[\]]+)\]\s*(#.*)?$`)
	keyRe := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=`)
	entry := key + " = " + value
	in, end := false, -1
	for i, l := range lines {
		if m := header.FindStringSubmatch(l); m != nil {
			if in {
				break
			}
			in = strings.TrimSpace(m[1]) == table
			if in {
				end = i + 1
			}
			continue
		}
		if !in {
			continue
		}
		if keyRe.MatchString(l) {
			lines[i] = entry
			return writeChecked(path, lines, raw)
		}
		if strings.TrimSpace(l) != "" {
			end = i + 1
		}
	}
	if end >= 0 {
		lines = append(lines[:end], append([]string{entry}, lines[end:]...)...)
	} else {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "["+table+"]", entry)
	}
	return writeChecked(path, lines, raw)
}

// writeChecked writes lines and loads the result; a file that no longer loads
// is put back.
func writeChecked(path string, lines []string, prev []byte) error {
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	if _, err := Load(path); err != nil {
		if prev == nil {
			_ = os.Remove(path)
		} else {
			_ = os.WriteFile(path, prev, 0o644)
		}
		return fmt.Errorf("config: edit refused, the file would not load: %w", err)
	}
	return nil
}

// UserName is the name the app shows: [user] name, else git config
// user.name, else the OS user.
func UserName(cfg *Config) string {
	if cfg != nil && strings.TrimSpace(cfg.User.Name) != "" {
		return strings.TrimSpace(cfg.User.Name)
	}
	if out, err := exec.Command("git", "config", "--global", "user.name").Output(); err == nil && strings.TrimSpace(string(out)) != "" {
		return strings.TrimSpace(string(out))
	}
	if u, err := user.Current(); err == nil {
		if u.Name != "" {
			return u.Name
		}
		return u.Username
	}
	return os.Getenv("USER")
}
