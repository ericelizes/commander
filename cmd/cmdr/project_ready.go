package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/example-user/commander/internal/activation"
	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/opencode"
	"github.com/example-user/commander/internal/store"
)

func ensureProjectManagerReady(project *store.Project) error {
	if project == nil {
		return fmt.Errorf("project lookup returned nil")
	}
	statePath, err := stateDBPath()
	if err != nil {
		return fmt.Errorf("resolve state dir: %w", err)
	}
	st, err := store.Open(statePath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	reg, err := agentmail.NewServerClientFromEnv()
	if err != nil {
		return fmt.Errorf("agent-mail client: %w", err)
	}
	a := activation.New(st, opencode.New(), reg, resolveCommanderRepo())
	if _, err := a.Activate(context.Background(), activation.ActivateRequest{
		Name: project.Name,
		Type: project.Type,
		Path: project.Path,
	}); err != nil {
		if hasProjectManagerSessionID(project.Path) {
			return nil
		}
		return fmt.Errorf("repair manager readiness for %q: %w", project.Name, err)
	}
	return nil
}

func hasProjectManagerSessionID(projectPath string) bool {
	commanderDir := filepath.Join(projectPath, ".commander")
	names := []string{"manager.session-id", "lead.session-id"}
	if agentrt.For(agentrt.TierLead) == agentrt.Claude {
		names = []string{agentrt.SessionFileName("manager", agentrt.Claude)}
	}
	for _, name := range names {
		body, err := os.ReadFile(filepath.Join(commanderDir, name))
		if err == nil && strings.TrimSpace(string(body)) != "" {
			return true
		}
	}
	return false
}
