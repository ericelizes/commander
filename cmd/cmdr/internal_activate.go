package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/example-user/commander/internal/binstage"
	"os"
	"path/filepath"
	"strings"

	"github.com/example-user/commander/internal/activation"
	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/opencode"
	"github.com/example-user/commander/internal/store"
)

// internalOpenProjectResult is the JSON shape printed to stdout on success.
// Field names mirror what the Commander prompt instructs the agent to parse.
type internalOpenProjectResult struct {
	ProjectID        int64  `json:"project_id"`
	ManagerSessionID string `json:"manager_session_id"`
	LeadSessionID    string `json:"lead_session_id"`
	AlreadyActive    bool   `json:"already_active"`
	AmAgentName      string `json:"am_agent_name"`
}

// internalOpenProjectError is the JSON shape printed to stderr on failure.
type internalOpenProjectError struct {
	Error string `json:"error"`
}

// runInternalOpenProject parses subcommand flags, opens the SQLite store, and
// runs the project-add orchestrator. Returns the process exit code.
//
// CLI shape (called by the Commander chat agent via Bash):
//
//	cmdr internal-add-project --name <slug> --type <code|knowledge> --path <abs>
//
// On success: stdout = single JSON line, exit 0.
// On failure: stderr = single JSON line {"error":"..."}, exit 1.
func runInternalOpenProject(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-add-project", flag.ContinueOnError)
	// Silence flag's own output; we emit JSON.
	fs.SetOutput(os.Stderr)

	name := fs.String("name", "", "project slug (lowercase letters/digits/hyphens)")
	typ := fs.String("type", "", "project type: code | knowledge")
	path := fs.String("path", "", "absolute path to project root")

	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	if *name == "" || *typ == "" || *path == "" {
		emitErr(stderr, "missing required flag(s): --name, --type, --path")
		return 1
	}

	statePath, err := stateDBPath()
	if err != nil {
		emitErr(stderr, fmt.Sprintf("resolve state dir: %v", err))
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		emitErr(stderr, fmt.Sprintf("mkdir %s: %v", filepath.Dir(statePath), err))
		return 1
	}

	st, err := store.Open(statePath)
	if err != nil {
		emitErr(stderr, fmt.Sprintf("open store: %v", err))
		return 1
	}
	defer st.Close()

	commanderRepo := resolveCommanderRepo()

	reg, err := agentmail.NewServerClientFromEnv()
	if err != nil {
		emitErr(stderr, fmt.Sprintf("agent-mail client: %v", err))
		return 1
	}
	a := activation.New(st, opencode.New(), reg, commanderRepo)
	res, err := a.Activate(context.Background(), activation.ActivateRequest{
		Name: *name,
		Type: *typ,
		Path: *path,
	})
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}

	out := internalOpenProjectResult{
		ProjectID:        res.ProjectID,
		ManagerSessionID: res.LeadSessionID,
		LeadSessionID:    res.LeadSessionID,
		AlreadyActive:    res.AlreadyActive,
		AmAgentName:      res.AmAgentName,
	}
	enc := json.NewEncoder(stdout)
	if err := enc.Encode(out); err != nil {
		emitErr(stderr, fmt.Sprintf("encode result: %v", err))
		return 1
	}
	return 0
}

// emitErr writes a single-line JSON error to w. Writes are best-effort —
// failure to write the error is logged to default stderr.
func emitErr(w *os.File, msg string) {
	b, err := json.Marshal(internalOpenProjectError{Error: msg})
	if err != nil {
		fmt.Fprintln(w, msg)
		return
	}
	fmt.Fprintln(w, string(b))
}

// stateDBPath returns the path to ~/.commander/state.db, honoring the
// COMMANDER_HOME override (matches the help text's documented env var).
func stateDBPath() (string, error) {
	if home := os.Getenv("COMMANDER_HOME"); home != "" {
		return filepath.Join(home, "state.db"), nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".commander", "state.db"), nil
}

// resolveCommanderRepo returns the absolute path to the commander checkout
// whose .opencode/agents directory should mount into Manager containers. Order:
//
//  1. $COMMANDER_REPO if set
//  2. walk up from the executable until a go.mod with the commander module
//     declaration is found
//  3. ~/code/commander as a sane default
func resolveCommanderRepo() string {
	if v := os.Getenv("COMMANDER_REPO"); v != "" {
		return v
	}
	if exe, err := os.Executable(); err == nil {
		if repo := binstage.RepoHint(exe); repo != "" {
			return repo
		}
		if root, ok := walkUpForGoMod(filepath.Dir(exe)); ok {
			return root
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "code", "commander")
	}
	return ""
}

// walkUpForGoMod walks up from start looking for a go.mod whose module line
// is the commander module. Returns ("", false) when not found within ~10 hops.
func walkUpForGoMod(start string) (string, bool) {
	cur := start
	for i := 0; i < 10; i++ {
		gomod := filepath.Join(cur, "go.mod")
		if body, err := os.ReadFile(gomod); err == nil {
			if strings.Contains(string(body), "module github.com/example-user/commander") {
				return cur, true
			}
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false
		}
		cur = parent
	}
	return "", false
}
