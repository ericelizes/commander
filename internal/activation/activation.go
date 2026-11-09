// Package activation implements U7's project-activation orchestrator: the
// pure-logic flow that turns a user intent ("activate frontend as code at
// /code/frontend") into a SQLite projects row + a Manager OpenCode session +
// an am-registered Manager agent + an idempotent .commander/ scratch dir.
//
// The TUI never calls into here directly. The Commander chat agent shells out
// to `cmdr internal-activate ...` (see cmd/cmdr), which constructs an Activator
// and runs Activate. Splitting the wiring this way keeps the orchestrator
// fully testable with mocks and lets the LLM-driven UX use the same code path
// as a future programmatic API.
package activation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/opencode"
	"github.com/example-user/commander/internal/store"
	promptassets "github.com/example-user/commander/prompts"
)

// ProjectStore is the slice of *store.Store this package depends on. Defined
// as an interface so tests substitute a fake without spinning up SQLite.
type ProjectStore interface {
	InsertProject(ctx context.Context, p store.Project) (int64, error)
	GetProjectByName(ctx context.Context, name string) (*store.Project, error)
	// UpsertAmAgent records the am server-assigned name for a (am project,
	// role) pair; GetAmAgent resolves it (or "" if unmapped). See
	// docs/agent-mail-routing.md.
	UpsertAmAgent(ctx context.Context, amProjectKey, role, amName string) error
	GetAmAgent(ctx context.Context, amProjectKey, role string) (string, error)
}

// Activator orchestrates the full activation flow. Construct via New.
type Activator struct {
	store         ProjectStore
	opencode      opencode.Client
	registrar     agentmail.AgentRegistrar
	commanderRepo string

	// now is overridable for deterministic tests; defaults to time.Now.
	now func() time.Time
}

// ActivateRequest is the user-supplied intent.
type ActivateRequest struct {
	// Name is the project's slug (lowercase letters, digits, hyphens).
	Name string
	// Type is "code" or "knowledge".
	Type string
	// Path is the absolute path to the project root. Must exist; for code
	// projects must contain a .git/ subdir.
	Path string
}

// ActivateResult summarizes the outcome. AlreadyActive is true on a
// re-activation hit (existing row, existing session-id file).
type ActivateResult struct {
	ProjectID         int64
	LeadSessionID     string
	LeadSessionIDPath string
	// AmAgentName is the role name (e.g. "manager-frontend"). AmAgentResolvedName
	// is the server-assigned random name it maps to (e.g. "GoldTiger").
	AmAgentName         string
	AmAgentResolvedName string
	AlreadyActive       bool
}

// New constructs an Activator. commanderRepo is the absolute path to this
// commander checkout — used to mount .opencode/agents into Manager containers
// (consumed by U9; recorded here for forward-compat).
func New(s ProjectStore, oc opencode.Client, reg agentmail.AgentRegistrar, commanderRepo string) *Activator {
	return &Activator{
		store:         s,
		opencode:      oc,
		registrar:     reg,
		commanderRepo: commanderRepo,
		now:           time.Now,
	}
}

// validTypes lists the accepted project types. Mirrors the SQLite CHECK
// constraint in 0001_initial.sql.
var validTypes = map[string]struct{}{
	"code":      {},
	"knowledge": {},
}

var (
	captureManagerSessionFn   = captureManagerBlankSession
	normalizeManagerSessionFn = normalizeManagerSession
	// newBeadsClientFn is overridable in tests so activation doesn't shell out
	// to a real `br`.
	newBeadsClientFn = beads.New
)

// Activate runs the full flow:
//
//  1. Validate the request (name slug, type, path exists, .git for code).
//  2. Insert into the projects table; on UNIQUE-name collision look up the
//     existing row + reuse its session-id file (idempotent re-activation).
//  3. Ensure <path>/.commander/ exists.
//  4. Read or capture the Manager session id from <path>/.commander/manager.session-id.
//     Capture means: invoke opencode.Run with no session id, then persist the
//     id from the returned RunResult.
//  5. Register manager-<flat-name> in the `commander` am project.
//  6. Append .commander/ and .worktrees/ to .git/info/exclude (idempotent).
//
// Errors are wrapped with the step that failed. If step 4+ fails, the SQLite
// row is intentionally NOT deleted — re-running Activate is the recovery path.
func (a *Activator) Activate(ctx context.Context, req ActivateRequest) (*ActivateResult, error) {
	if err := validateRequest(req); err != nil {
		return nil, err
	}

	flat := flatName(req.Name)
	managerAgent := ManagerAgentName(req.Name)
	legacyLeadAgent := "lead-" + flat
	commanderDir := filepath.Join(req.Path, ".commander")
	sessionFile := filepath.Join(commanderDir, "manager.session-id")
	legacySessionFile := filepath.Join(commanderDir, "lead.session-id")

	// Step 2: insert; on unique-name collision look up existing row.
	projectID, alreadyActive, err := a.upsertProject(ctx, req)
	if err != nil {
		return nil, err
	}

	// Step 3: scratch dir.
	if err := os.MkdirAll(commanderDir, 0o755); err != nil {
		return nil, fmt.Errorf("activation: create %s: %w", commanderDir, err)
	}

	// Step 3.5: initialize the project's beads workspace so the Manager can
	// create Objectives/Tasks immediately. Idempotent (no-op if already init'd).
	if err := newBeadsClientFn().Init(ctx, req.Path); err != nil {
		return nil, fmt.Errorf("activation: init beads: %w", err)
	}

	// Step 4: capture-or-resume Manager session.
	var sessionID string
	var sessionReused bool
	if agentrt.For(agentrt.TierLead) == agentrt.Claude {
		sessionFile = filepath.Join(commanderDir, agentrt.SessionFileName("manager", agentrt.Claude))
		sessionID, sessionReused, err = ensureClaudeManagerSession(sessionFile, managerAgent)
	} else {
		sessionID, sessionReused, err = a.ensureManagerSession(ctx, req, sessionFile, legacySessionFile, managerAgent, legacyLeadAgent)
	}
	if err != nil {
		return nil, err
	}

	// alreadyActive is true if either signal fires: SQLite already had the
	// row, OR the session-id file pre-existed (project's .commander survived
	// a DB wipe).
	if sessionReused {
		alreadyActive = true
	}

	// Step 5: am registration via the server. The `am` CLI cannot run while the
	// always-on am server holds the mailbox DB, and the server assigns random
	// agent names (rejecting role names like "manager-foo"). So we register once
	// through the server and persist a role->name mapping; on re-activation we
	// reuse the existing mapping rather than registering a duplicate. Both the
	// manager role and the legacy lead alias point at the one agent.
	// See docs/agent-mail-routing.md.
	amProjectKey := a.commanderRepo
	if amProjectKey == "" {
		return nil, fmt.Errorf("activation: am registration requires the commander repo path (set COMMANDER_REPO)")
	}
	resolvedAmName, err := a.store.GetAmAgent(ctx, amProjectKey, managerAgent)
	if err != nil {
		return nil, fmt.Errorf("activation: look up am agent for %s: %w", managerAgent, err)
	}
	if resolvedAmName == "" {
		if _, err := a.registrar.EnsureProject(ctx, amProjectKey); err != nil {
			return nil, fmt.Errorf("activation: ensure am project: %w", err)
		}
		resolvedAmName, err = a.registrar.RegisterAgent(ctx, amProjectKey, "opencode-ai", "sonnet", "manager for "+req.Name)
		if err != nil {
			return nil, fmt.Errorf("activation: register am agent for %s: %w", req.Name, err)
		}
		if err := a.store.UpsertAmAgent(ctx, amProjectKey, managerAgent, resolvedAmName); err != nil {
			return nil, fmt.Errorf("activation: map %s: %w", managerAgent, err)
		}
		if err := a.store.UpsertAmAgent(ctx, amProjectKey, legacyLeadAgent, resolvedAmName); err != nil {
			return nil, fmt.Errorf("activation: map %s: %w", legacyLeadAgent, err)
		}
	}

	// Step 6: gitignore-style exclude entries (only meaningful for code projects
	// where .git/info/exclude exists; silently skipped otherwise).
	if err := appendGitExcludes(req.Path); err != nil {
		return nil, fmt.Errorf("activation: update git exclude: %w", err)
	}

	return &ActivateResult{
		ProjectID:           projectID,
		LeadSessionID:       sessionID,
		LeadSessionIDPath:   sessionFile,
		AmAgentName:         managerAgent,
		AmAgentResolvedName: resolvedAmName,
		AlreadyActive:       alreadyActive,
	}, nil
}

// upsertProject inserts the project and returns its rowid. On UNIQUE-name
// collision it re-fetches the existing row and returns alreadyActive=true.
// Any other error is propagated.
func (a *Activator) upsertProject(ctx context.Context, req ActivateRequest) (int64, bool, error) {
	id, err := a.store.InsertProject(ctx, store.Project{
		Name:        req.Name,
		Type:        req.Type,
		Path:        req.Path,
		ActivatedAt: a.now().Unix(),
		Status:      "active",
	})
	if err == nil {
		return id, false, nil
	}
	// SQLite surfaces the unique violation in the error text.
	if !strings.Contains(err.Error(), "UNIQUE") {
		return 0, false, fmt.Errorf("activation: insert project: %w", err)
	}
	existing, lookupErr := a.store.GetProjectByName(ctx, req.Name)
	if lookupErr != nil {
		return 0, false, fmt.Errorf("activation: lookup existing project %q: %w", req.Name, lookupErr)
	}
	if existing == nil {
		// Race: insert says dupe, lookup says missing. Re-surface original.
		return 0, false, fmt.Errorf("activation: insert project: %w", err)
	}
	return existing.ID, true, nil
}

// ensureManagerSession returns the session id for the project's Manager. The
// reused flag is true when the id came from an existing session-id file
// (idempotent re-activation); false when this call captured a fresh id from
// opencode.
func (a *Activator) ensureManagerSession(ctx context.Context, req ActivateRequest, sessionFile, legacySessionFile, managerAgent, legacyLeadAgent string) (string, bool, error) {
	if existing, ok, err := readSessionFile(sessionFile); err != nil {
		return "", false, fmt.Errorf("activation: read %s: %w", sessionFile, err)
	} else if ok {
		normalized, migrated, err := normalizeManagerSessionFn(ctx, req, existing)
		if err != nil {
			return "", false, fmt.Errorf("activation: normalize %s: %w", sessionFile, err)
		}
		if migrated {
			if err := writeSessionFile(sessionFile, normalized); err != nil {
				return "", false, fmt.Errorf("activation: persist manager session id: %w", err)
			}
			if err := writeSessionFile(legacySessionFile, normalized); err != nil {
				return "", false, fmt.Errorf("activation: persist legacy session id: %w", err)
			}
		}
		return normalized, true, nil
	}
	if existing, ok, err := readSessionFile(legacySessionFile); err != nil {
		return "", false, fmt.Errorf("activation: read %s: %w", legacySessionFile, err)
	} else if ok {
		normalized, migrated, err := normalizeManagerSessionFn(ctx, req, existing)
		if err != nil {
			return "", false, fmt.Errorf("activation: normalize %s: %w", legacySessionFile, err)
		}
		if err := writeSessionFile(sessionFile, normalized); err != nil {
			return "", false, fmt.Errorf("activation: persist manager session id: %w", err)
		}
		if migrated {
			if err := writeSessionFile(legacySessionFile, normalized); err != nil {
				return "", false, fmt.Errorf("activation: persist legacy session id: %w", err)
			}
		}
		return normalized, true, nil
	}
	if err := promptassets.EnsureManagerAgent(managerAgent); err != nil {
		return "", false, fmt.Errorf("activation: ensure manager agent prompt: %w", err)
	}
	if err := promptassets.EnsureLeadAgent(legacyLeadAgent); err != nil {
		return "", false, fmt.Errorf("activation: ensure legacy lead agent prompt: %w", err)
	}

	sessionID, err := captureManagerSessionFn(ctx, req, managerAgent)
	if err != nil {
		return "", false, fmt.Errorf("activation: bootstrap Manager session: %w", err)
	}
	if err := writeSessionFile(sessionFile, sessionID); err != nil {
		return "", false, fmt.Errorf("activation: persist session id: %w", err)
	}
	if err := writeSessionFile(legacySessionFile, sessionID); err != nil {
		return "", false, fmt.Errorf("activation: persist legacy session id: %w", err)
	}
	return sessionID, false, nil
}

// ensureClaudeManagerSession is ensureManagerSession for the Claude runtime:
// install the Claude agent and mint a UUID. There is no blank-session import
// or bootstrap migration because Claude sessions start empty.
func ensureClaudeManagerSession(sessionFile, managerAgent string) (string, bool, error) {
	if err := promptassets.EnsureClaudeAgent(promptassets.RoleManager, managerAgent); err != nil {
		return "", false, fmt.Errorf("activation: ensure claude manager agent: %w", err)
	}
	if existing, ok, err := readSessionFile(sessionFile); err != nil {
		return "", false, fmt.Errorf("activation: read %s: %w", sessionFile, err)
	} else if ok {
		return existing, true, nil
	}
	sessionID := agentrt.NewClaudeSessionID()
	if err := writeSessionFile(sessionFile, sessionID); err != nil {
		return "", false, fmt.Errorf("activation: persist session id: %w", err)
	}
	return sessionID, false, nil
}

// ManagerAgentName is the agent name for a project's Manager, shared by the
// opencode and Claude agent files and the layout pane that opens it.
func ManagerAgentName(projectName string) string {
	return "manager-" + flatName(projectName)
}

// readSessionFile returns (id, true, nil) when the file exists with a non-empty
// trimmed body. Missing file → ("", false, nil). Other I/O errors propagate.
func readSessionFile(path string) (string, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	id := strings.TrimSpace(string(b))
	if id == "" {
		return "", false, nil
	}
	return id, true, nil
}

// writeSessionFile persists the session id with mode 0644. Uses a temp file +
// rename so a crashed write never leaves a half-written id behind.
func writeSessionFile(path, id string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".manager-session-id-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // safe even after successful rename: rename invalidates the path.
	if _, err := io.WriteString(tmp, id+"\n"); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// validateRequest checks Name/Type/Path. Returned errors are user-facing.
func validateRequest(req ActivateRequest) error {
	if !slugSafe(req.Name) {
		return fmt.Errorf("activation: name %q must be lowercase letters, digits, and hyphens", req.Name)
	}
	if _, ok := validTypes[req.Type]; !ok {
		return fmt.Errorf("activation: type %q must be one of {code, knowledge}", req.Type)
	}
	if !filepath.IsAbs(req.Path) {
		return fmt.Errorf("activation: path %q must be absolute", req.Path)
	}
	info, err := os.Stat(req.Path)
	if err != nil {
		return fmt.Errorf("activation: path %q: %w", req.Path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("activation: path %q is not a directory", req.Path)
	}
	if req.Type == "code" {
		gitDir := filepath.Join(req.Path, ".git")
		if _, err := os.Stat(gitDir); err != nil {
			return fmt.Errorf("activation: path %q is not a git repo (.git missing)", req.Path)
		}
	}
	return nil
}

// slugSafe returns true when s is non-empty and contains only [a-z0-9-].
func slugSafe(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return false
		}
	}
	return true
}

// flatName lowercases and replaces '/'/'_'/' ' with '-'. Names that already
// pass slugSafe are returned untouched. This is the form embedded into
// am agent names (lead-<flat>, operator-<flat-branch>).
func flatName(name string) string {
	out := strings.ToLower(name)
	out = strings.ReplaceAll(out, "/", "-")
	out = strings.ReplaceAll(out, "_", "-")
	out = strings.ReplaceAll(out, " ", "-")
	return out
}

type managerBlankSession struct {
	Info     managerBlankSessionInfo `json:"info"`
	Messages []any                   `json:"messages"`
}

type managerBlankSessionInfo struct {
	ID         string                    `json:"id"`
	Slug       string                    `json:"slug"`
	ProjectID  string                    `json:"projectID"`
	Directory  string                    `json:"directory"`
	Path       string                    `json:"path"`
	Title      string                    `json:"title"`
	Version    string                    `json:"version"`
	Summary    managerBlankSessionCounts `json:"summary"`
	Permission []managerBlankSessionPerm `json:"permission"`
	Time       managerBlankSessionTime   `json:"time"`
}

type managerBlankSessionCounts struct {
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
	Files     int `json:"files"`
}

type managerBlankSessionPerm struct {
	Permission string `json:"permission"`
	Pattern    string `json:"pattern"`
	Action     string `json:"action"`
}

type managerBlankSessionTime struct {
	Created int64 `json:"created"`
	Updated int64 `json:"updated"`
}

type exportedSession struct {
	Messages []exportedMessage `json:"messages"`
}

type exportedMessage struct {
	Info struct {
		Role string `json:"role"`
	} `json:"info"`
	Parts []exportedPart `json:"parts"`
}

type exportedPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func captureManagerBlankSession(ctx context.Context, req ActivateRequest, _ string) (string, error) {
	sessionID := fmt.Sprintf("ses_mgr_%d", time.Now().UnixNano())
	payload, err := marshalManagerBlankSession(sessionID, req, time.Now())
	if err != nil {
		return "", fmt.Errorf("marshal manager blank session: %w", err)
	}
	tmp, err := os.CreateTemp("", "cmdr-manager-blank-session-*.json")
	if err != nil {
		return "", fmt.Errorf("create temp session file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write temp session file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close temp session file: %w", err)
	}

	cmd := exec.CommandContext(ctx, "opencode", "import", tmp.Name())
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("opencode import: %w: %s", err, strings.TrimSpace(string(out)))
	}
	importedID, err := parseImportedSessionID(string(out))
	if err != nil {
		return "", fmt.Errorf("opencode import: %w", err)
	}
	return importedID, nil
}

func normalizeManagerSession(ctx context.Context, req ActivateRequest, sessionID string) (string, bool, error) {
	raw, err := exportOpencodeSession(ctx, sessionID)
	if err != nil {
		return sessionID, false, nil
	}
	if !sessionNeedsManagerBootstrapMigration(raw) {
		return sessionID, false, nil
	}
	migratedSessionID, err := captureManagerBlankSession(ctx, req, "")
	if err != nil {
		return "", false, fmt.Errorf("capture blank replacement session: %w", err)
	}
	return migratedSessionID, true, nil
}

func marshalManagerBlankSession(sessionID string, req ActivateRequest, now time.Time) ([]byte, error) {
	path := strings.TrimPrefix(strings.ReplaceAll(req.Path, "\\", "/"), "/")
	created := now.UnixMilli()
	return json.MarshalIndent(managerBlankSession{
		Info: managerBlankSessionInfo{
			ID:        sessionID,
			Slug:      "manager-" + req.Name,
			ProjectID: req.Name,
			Directory: req.Path,
			Path:      path,
			Title:     "Manager · " + req.Name,
			Version:   "0",
			Summary: managerBlankSessionCounts{
				Additions: 0,
				Deletions: 0,
				Files:     0,
			},
			Permission: []managerBlankSessionPerm{
				{Permission: "question", Pattern: "*", Action: "deny"},
				{Permission: "plan_enter", Pattern: "*", Action: "deny"},
				{Permission: "plan_exit", Pattern: "*", Action: "deny"},
			},
			Time: managerBlankSessionTime{
				Created: created,
				Updated: created,
			},
		},
		Messages: []any{},
	}, "", "  ")
}

func exportOpencodeSession(ctx context.Context, sessionID string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "opencode", "export", sessionID)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("opencode export: %w: %s", err, strings.TrimSpace(string(out)))
	}
	lines := strings.Split(string(out), "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "{") {
			return []byte(strings.Join(lines[i:], "\n")), nil
		}
	}
	return nil, fmt.Errorf("opencode export: missing JSON payload")
}

func sessionNeedsManagerBootstrapMigration(raw []byte) bool {
	var sess exportedSession
	if err := json.Unmarshal(raw, &sess); err != nil {
		return false
	}
	if len(sess.Messages) != 2 {
		return false
	}
	if sess.Messages[0].Info.Role != "user" || sess.Messages[1].Info.Role != "assistant" {
		return false
	}
	userText := exportedMessageText(sess.Messages[0])
	assistantText := exportedMessageText(sess.Messages[1])
	if !strings.Contains(userText, "This is your bootstrap session.") {
		return false
	}
	if !strings.Contains(userText, "You are the Lead for project") && !strings.Contains(userText, "You are the Manager for project") {
		return false
	}
	return strings.TrimSpace(assistantText) != ""
}

func exportedMessageText(msg exportedMessage) string {
	var parts []string
	for _, part := range msg.Parts {
		if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func parseImportedSessionID(out string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Imported session:") {
			continue
		}
		id := strings.TrimSpace(strings.TrimPrefix(line, "Imported session:"))
		if id != "" {
			return id, nil
		}
	}
	return "", fmt.Errorf("missing imported session id in output %q", strings.TrimSpace(out))
}

// appendGitExcludes appends `.commander/`, `.worktrees`, `.worktrees/` and `.worktrees.noindex/` to
// <path>/.git/info/exclude when missing. No-op when .git/info/exclude does not
// exist (knowledge-type projects without git, or fresh git repos that haven't
// initialized info/exclude).
func appendGitExcludes(projectPath string) error {
	excludePath := filepath.Join(projectPath, ".git", "info", "exclude")
	body, err := os.ReadFile(excludePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	want := []string{".commander/", ".worktrees", ".worktrees/", ".worktrees.noindex/"}
	existing := string(body)
	var toAdd []string
	for _, w := range want {
		if !containsLine(existing, w) {
			toAdd = append(toAdd, w)
		}
	}
	if len(toAdd) == 0 {
		return nil
	}
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	// Make sure we start on a fresh line.
	if len(existing) > 0 && !strings.HasSuffix(existing, "\n") {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	for _, line := range toAdd {
		if _, err := f.WriteString(line + "\n"); err != nil {
			return err
		}
	}
	return nil
}

// containsLine reports whether body has a line matching needle exactly
// (ignoring leading/trailing whitespace on each line).
func containsLine(body, needle string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == needle {
			return true
		}
	}
	return false
}
