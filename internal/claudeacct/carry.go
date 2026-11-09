package claudeacct

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// FindTranscript returns the transcript path of sessionID under projectsDir, or
// "" when Claude has none (the session never received a message).
func FindTranscript(projectsDir, sessionID string) string {
	matches, _ := filepath.Glob(filepath.Join(projectsDir, "*", sessionID+".jsonl"))
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// TmuxConfigDir is the CLAUDE_CONFIG_DIR of the tmux server run talks to, "" when
// it has none. A pane inherits the server's value, which may differ from the
// caller's own. run takes tmux's argv, without the binary.
func TmuxConfigDir(ctx context.Context, run func(context.Context, ...string) ([]byte, []byte, error)) string {
	out, _, err := run(ctx, "show-environment", "-g", "CLAUDE_CONFIG_DIR")
	if err != nil {
		return ""
	}
	dir, ok := strings.CutPrefix(strings.TrimSpace(string(out)), "CLAUDE_CONFIG_DIR=")
	if !ok || !filepath.IsAbs(dir) {
		return ""
	}
	return dir
}

// CarryTranscript copies the newest transcript of sessionID, wherever it last
// ran, into account's projects dir under the same cwd slug, and returns the path
// to resume from ("" when no dir has the session). It searches every account
// (defaultProjects is the default account's) and the config dirs a child may
// have inherited: this process's CLAUDE_CONFIG_DIR and inherited. A pane that ran
// under one wrote its transcript there. Call it just before a real launch, never
// for a chat left running: that chat's next write would make this copy stale.
func CarryTranscript(home, defaultProjects, sessionID, account string, inherited ...string) (string, error) {
	dst := defaultProjects
	if account != "" && account != Default {
		dst = ProjectsDir(home, account)
	}
	dirs := []string{defaultProjects}
	for _, a := range List(home)[1:] {
		dirs = append(dirs, ProjectsDir(home, a.Name))
	}
	for _, d := range append(inherited, os.Getenv("CLAUDE_CONFIG_DIR")) {
		if d != "" {
			dirs = append(dirs, filepath.Join(d, "projects"))
		}
	}
	newest, newestAt := "", int64(0)
	for _, d := range dirs {
		p := FindTranscript(d, sessionID)
		if p == "" {
			continue
		}
		if fi, err := os.Stat(p); err == nil && fi.ModTime().UnixNano() > newestAt {
			newest, newestAt = p, fi.ModTime().UnixNano()
		}
	}
	if newest == "" || filepath.Dir(filepath.Dir(newest)) == filepath.Clean(dst) {
		return newest, nil
	}
	to := filepath.Join(dst, filepath.Base(filepath.Dir(newest)), filepath.Base(newest))
	if err := copyFile(newest, to); err != nil {
		return "", fmt.Errorf("claudeacct: carry transcript to %s: %w", to, err)
	}
	return to, nil
}

func copyFile(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := to + ".cmdr-tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, to)
}
