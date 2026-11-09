package managerguard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MailInterval is how often one Manager's refusals mail the Commander.
const MailInterval = 30 * time.Minute

// Note is the agent_state note a refused Manager carries in the sidebar.
const Note = "⚠ Manager doing work"

// LogRefusal appends one refusal to <home>/logs/manager-refusals.jsonl.
func LogRefusal(home, role, command, why string, now time.Time) error {
	line, err := json.Marshal(map[string]any{"role": role, "command": command, "why": why, "time": now.UTC().Format(time.RFC3339)})
	if err != nil {
		return err
	}
	return appendLine(filepath.Join(home, "logs", "manager-refusals.jsonl"), line)
}

// LogError records a hook failure (the hook itself fails open).
func LogError(home, role, msg string, now time.Time) {
	line, _ := json.Marshal(map[string]any{"role": role, "error": msg, "time": now.UTC().Format(time.RFC3339)})
	_ = appendLine(filepath.Join(home, "logs", "manager-guard-errors.jsonl"), line)
}

func appendLine(path string, line []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// ShouldMail reports whether the Commander has not been mailed about role in
// the last MailInterval, and if so records now as the last mail.
func ShouldMail(home, role string, now time.Time) bool {
	path := filepath.Join(home, "logs", "manager-refusal-mail", role)
	if raw, err := os.ReadFile(path); err == nil {
		if last, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err == nil &&
			now.Sub(time.Unix(last, 0)) < MailInterval {
			return false
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false
	}
	return os.WriteFile(path, []byte(strconv.FormatInt(now.Unix(), 10)), 0o644) == nil
}
