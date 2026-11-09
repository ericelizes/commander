package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadLocal(t *testing.T) {
	for _, tt := range []struct {
		name, content, workspace string
		bad                      bool
	}{
		{name: "missing"},
		{name: "empty", content: "# optional"},
		{name: "workspace", content: "[integrations.linear]\nworkspace = 'acme'", workspace: "acme"},
		{name: "unknown", content: "[integrations.linear]\nworkspce = 'acme'", bad: true},
		{name: "malformed", content: "[integrations", bad: true},
		{name: "URL", content: "[integrations.linear]\nworkspace = 'https://example.com'", bad: true},
		{name: "path", content: "[integrations.linear]\nworkspace = '../other'", bad: true},
		{name: "type", content: "[integrations.linear]\nworkspace = 3", bad: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.content != "" {
				if err := os.WriteFile(filepath.Join(dir, "local.toml"), []byte(tt.content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := LoadLocal(filepath.Join(dir, "config.toml"))
			if (err != nil) != tt.bad {
				t.Fatalf("LoadLocal = %+v, %v", got, err)
			}
			if !tt.bad && got.Integrations.Linear.Workspace != tt.workspace {
				t.Fatalf("workspace = %q", got.Integrations.Linear.Workspace)
			}
		})
	}
}

func TestLinearTeamOptional(t *testing.T) {
	for _, content := range []string{"", "[integrations.linear]\nteam=''", "[integrations.linear]\nteam='ABC'"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "local.toml"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		local, err := LoadLocal(filepath.Join(dir, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		team := local.Integrations.Linear.Team
		if content == "" {
			if team != nil {
				t.Fatal("absent team is not nil")
			}
		} else if team == nil {
			t.Fatal("explicit team is nil")
		} else if strings.Contains(content, "ABC") && *team != "ABC" {
			t.Fatalf("team = %q", *team)
		}
	}
}
