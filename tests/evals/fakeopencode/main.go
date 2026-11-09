package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/example-user/commander/internal/store"
)

type event struct {
	Type      string      `json:"type"`
	Timestamp int64       `json:"timestamp"`
	SessionID string      `json:"sessionID"`
	Part      interface{} `json:"part"`
}

type provider struct {
	pending *pendingAction
}

type pendingAction struct {
	command []string
}

type workspaceMatch struct {
	Name string
	Path string
	Type string
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "fakeopencode: missing args")
		os.Exit(1)
	}

	if args[0] == "run" {
		if err := runCapture(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		return
	}
	if args[0] == "import" {
		if err := runImport(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		return
	}
	if args[0] == "export" {
		if err := runExport(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		return
	}

	if args[0] == "--session" && len(args) >= 2 {
		runInteractive(args[1])
		return
	}

	fmt.Fprintf(os.Stderr, "fakeopencode: unsupported args: %s\n", strings.Join(args, " "))
	os.Exit(1)
}

func runCapture(args []string) error {
	agent := ""
	prompt := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--agent":
			if i+1 < len(args) {
				agent = args[i+1]
				i++
			}
		default:
			if !strings.HasPrefix(args[i], "--") {
				prompt = args[i]
			}
		}
	}
	sessionID := "ses_" + sanitize(agent)
	appendLine(os.Getenv("FAKE_OPENCODE_RUN_LOG"), fmt.Sprintf("agent=%s prompt=%s", agent, prompt))
	ev1 := event{
		Type:      "text",
		Timestamp: 1,
		SessionID: sessionID,
		Part: map[string]string{
			"text": "ok",
		},
	}
	ev2 := event{
		Type:      "step_finish",
		Timestamp: 2,
		SessionID: sessionID,
		Part: map[string]any{
			"cost": 0,
			"tokens": map[string]int{
				"input":  1,
				"output": 1,
			},
		},
	}
	for _, ev := range []event{ev1, ev2} {
		body, _ := json.Marshal(ev)
		fmt.Println(string(body))
	}
	return nil
}

func runImport(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("fakeopencode: import expects a single file argument")
	}
	body, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	var payload struct {
		Info struct {
			ID string `json:"id"`
		} `json:"info"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	sessionID := strings.TrimSpace(payload.Info.ID)
	if sessionID == "" {
		sessionID = "ses_imported"
	}
	fmt.Printf("Imported session: %s\n", sessionID)
	return nil
}

func runExport(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("fakeopencode: export expects a single session id")
	}
	payload := map[string]any{
		"info": map[string]any{
			"id": args[0],
		},
		"messages": []any{},
	}
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	fmt.Printf("Exporting session: %s\n%s\n", args[0], body)
	return nil
}

func runInteractive(sessionID string) {
	p := &provider{}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		msg := strings.TrimSpace(scanner.Text())
		if msg == "" {
			continue
		}
		appendLine(os.Getenv("FAKE_OPENCODE_TRANSCRIPT_LOG"), "user: "+msg)
		reply := p.handle(strings.TrimSpace(msg))
		fmt.Println(reply)
		appendLine(os.Getenv("FAKE_OPENCODE_TRANSCRIPT_LOG"), "assistant: "+strings.ReplaceAll(reply, "\n", " "))
	}
	_ = sessionID
}

func (p *provider) handle(msg string) string {
	if p.pending != nil {
		switch strings.ToLower(strings.TrimSpace(msg)) {
		case "y", "yes", "go", "do it":
			reply := executeCmdr(p.pending.command)
			p.pending = nil
			return reply
		case "n", "no":
			p.pending = nil
			return "Understood. I did not run it."
		}
	}

	lower := strings.ToLower(strings.TrimSpace(msg))
	switch {
	case lower == "show user-state options":
		return executeCmdr([]string{"internal-set-objective", "--help"})
	case lower == "who are you":
		return "I am Commander for the commander workspace."
	case strings.HasPrefix(lower, "show me task "):
		taskID := strings.TrimSpace(msg[len("show me task "):])
		return executeCmdr([]string{"--task", taskID})
	case strings.HasPrefix(lower, "show me "):
		project := strings.TrimSpace(msg[len("show me "):])
		return executeCmdr([]string{"--project", project})
	}

	if action, ok := parseExplicitAdd(msg); ok {
		p.pending = &pendingAction{command: action}
		return "I'll run: cmdr " + strings.Join(action, " ") + "\nShould I proceed? (y/n)"
	}
	if strings.HasPrefix(lower, "activate ") || strings.HasPrefix(lower, "open ") || strings.HasPrefix(lower, "add ") {
		query := strings.TrimSpace(msg[len(pathlessAddPrefix(lower)):])
		resolution, err := resolveActivation(query)
		if err != nil {
			return err.Error()
		}
		if resolution.AlreadyActive && len(resolution.Matches) == 1 {
			return fmt.Sprintf("%s is already added at %s.", normalizeProjectName(query), resolution.Matches[0].Path)
		}
		if resolution.Root != "" {
			appendLine(os.Getenv("FAKE_OPENCODE_SEARCH_LOG"), fmt.Sprintf("query=%s root=%s", query, resolution.Root))
		}
		switch len(resolution.Matches) {
		case 0:
			return fmt.Sprintf("I couldn't find %q under %s. What path should I use?", strings.TrimSpace(query), resolution.Root)
		case 1:
			match := resolution.Matches[0]
			cmd := []string{"internal-add-project", "--name", normalizeProjectName(match.Name), "--type", match.Type, "--path", match.Path}
			p.pending = &pendingAction{command: cmd}
			return "I'll run: cmdr " + strings.Join(cmd, " ") + "\nShould I proceed? (y/n)"
		default:
			paths := make([]string, 0, len(resolution.Matches))
			for _, match := range resolution.Matches {
				paths = append(paths, match.Path)
			}
			return fmt.Sprintf("I found multiple matches for %q: %s. Which one should I add?", normalizeProjectName(query), strings.Join(paths, ", "))
		}
	}

	return "I didn't recognize that request."
}

func parseExplicitAdd(msg string) ([]string, bool) {
	parts := strings.Fields(strings.TrimSpace(msg))
	if len(parts) < 6 {
		return nil, false
	}
	verb := strings.ToLower(parts[0])
	if verb != "activate" && verb != "open" && verb != "add" {
		return nil, false
	}
	atIdx := -1
	asIdx := -1
	for i, part := range parts {
		switch strings.ToLower(part) {
		case "as":
			asIdx = i
		case "at":
			atIdx = i
		}
	}
	if asIdx != 2 || atIdx != 4 || len(parts) != 6 {
		return nil, false
	}
	return []string{
		"internal-add-project",
		"--name", normalizeProjectName(parts[1]),
		"--type", strings.ToLower(parts[3]),
		"--path", parts[5],
	}, true
}

func pathlessAddPrefix(lower string) string {
	if strings.HasPrefix(lower, "add ") {
		return "add "
	}
	if strings.HasPrefix(lower, "open ") {
		return "open "
	}
	return "activate "
}

func executeCmdr(args []string) string {
	appendLine(os.Getenv("FAKE_OPENCODE_ACTION_LOG"), "cmdr "+strings.Join(args, " "))
	cmd := exec.Command("cmdr", args...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(out))
	if err != nil {
		if trimmed == "" {
			return fmt.Sprintf("Command failed: %v", err)
		}
		return trimmed
	}

	switch {
	case len(args) > 0 && (args[0] == "internal-add-project" || args[0] == "internal-open-project"):
		var payload struct {
			ManagerSessionID string `json:"manager_session_id"`
			LeadSessionID    string `json:"lead_session_id"`
			AlreadyActive    bool   `json:"already_active"`
		}
		if json.Unmarshal(out, &payload) == nil {
			sessionID := payload.ManagerSessionID
			if sessionID == "" {
				sessionID = payload.LeadSessionID
			}
			if sessionID == "" {
				return trimmed
			}
			if payload.AlreadyActive {
				return fmt.Sprintf("Already added. Reusing Manager session: %s", sessionID)
			}
			return fmt.Sprintf("Added. Manager session captured: %s", sessionID)
		}
		return trimmed
	case len(args) > 1 && args[0] == "--project":
		return fmt.Sprintf("Opened project %s.", args[1])
	case len(args) > 1 && args[0] == "--task":
		return fmt.Sprintf("Opened task %s.", args[1])
	default:
		return trimmed
	}
}

func searchWorkspace(query string) ([]workspaceMatch, string) {
	root := os.Getenv("COMMANDER_WORKSPACE_ROOT")
	if root == "" {
		root, _ = os.Getwd()
	}
	want := normalizeProjectName(query)
	var matches []workspaceMatch
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if shouldSkipDir(base) {
			return filepath.SkipDir
		}
		if normalizeProjectName(base) != want {
			return nil
		}
		projectType := "knowledge"
		if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
			projectType = "code"
		}
		matches = append(matches, workspaceMatch{Name: base, Path: path, Type: projectType})
		return filepath.SkipDir
	})
	sort.Slice(matches, func(i, j int) bool { return matches[i].Path < matches[j].Path })
	return matches, root
}

func normalizeProjectName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.TrimPrefix(name, "the ")
	name = strings.TrimSuffix(name, " project")
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ReplaceAll(name, " ", "-")
	return strings.Trim(name, "-")
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".commander", "node_modules", "vendor":
		return true
	}
	return strings.HasPrefix(name, ".")
}

func sanitize(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return "session"
	}
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "_", "-")
	return strings.ReplaceAll(s, " ", "-")
}

func lookupActiveProject(query string) (string, bool) {
	home := os.Getenv("COMMANDER_HOME")
	if home == "" {
		return "", false
	}
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		return "", false
	}
	defer st.Close()

	project, err := st.GetProjectByName(context.Background(), normalizeProjectName(query))
	if err != nil || project == nil || project.Path == "" {
		return "", false
	}
	return project.Path, true
}

type activationResolution struct {
	AlreadyActive bool             `json:"already_active"`
	Root          string           `json:"root"`
	Matches       []workspaceMatch `json:"matches"`
}

func resolveActivation(query string) (activationResolution, error) {
	appendLine(os.Getenv("FAKE_OPENCODE_ACTION_LOG"), "cmdr internal-resolve-add-project --name "+normalizeProjectName(query))
	cmd := exec.Command("cmdr", "internal-resolve-add-project", "--name", normalizeProjectName(query))
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return activationResolution{}, fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	var res activationResolution
	if err := json.Unmarshal(out, &res); err != nil {
		return activationResolution{}, err
	}
	return res, nil
}

func appendLine(path, line string) {
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line + "\n")
}
