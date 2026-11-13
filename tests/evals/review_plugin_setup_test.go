package evals

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise setup/preflight against private plugin records without installing
// plugins, launching models or making review claims.
func TestReviewPluginSetupScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/review-plugin-setup.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Cases []struct {
			Name                           string
			Installed, Required, Continues bool
		}
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	if len(scenario.Cases) == 0 {
		t.Fatal("empty scenario")
	}
	script, err := filepath.Abs("../../install/check-review-plugin.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range scenario.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
			if tc.Installed {
				plugin := filepath.Join(home, "plugin")
				skill := filepath.Join(plugin, "skills", "ce-code-review")
				if err := os.MkdirAll(skill, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("fixture"), 0644); err != nil {
					t.Fatal(err)
				}
				dir := filepath.Join(home, ".claude", "plugins")
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				registry, _ := json.Marshal(map[string]any{"version": 2, "plugins": map[string]any{"compound-engineering@fixture": []any{map[string]string{"installPath": plugin}}}})
				if err := os.WriteFile(filepath.Join(dir, "installed_plugins.json"), registry, 0644); err != nil {
					t.Fatal(err)
				}
			}
			mode := ""
			if tc.Required {
				mode = "--required"
			}
			cmd := exec.Command("bash", "-c", `bash "$1" ${2:+"$2"} && echo CONTINUED`, "preflight", script, mode)
			out, err := cmd.CombinedOutput()
			continued := strings.Contains(string(out), "CONTINUED")
			if continued != tc.Continues || (err != nil) == tc.Continues {
				t.Fatalf("continued=%v err=%v: %s", continued, err, out)
			}
			if !tc.Installed && !strings.Contains(string(out), "claude plugin install compound-engineering") {
				t.Fatalf("no setup guidance: %s", out)
			}
		})
	}
}
