package install

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewPluginCheck(t *testing.T) {
	for _, tc := range []struct {
		name, registry          string
		skill, required, wantOK bool
	}{
		{name: "fresh install"},
		{name: "required on fresh install", required: true},
		{name: "corrupt registry", registry: "{"},
		{name: "empty registry", registry: `{"version":2,"plugins":{}}`},
		{name: "unrelated plugin", registry: `{"plugins":{"other@market":[{"installPath":"PATH"}]}}`, skill: true},
		{name: "stale installation", registry: `{"plugins":{"compound-engineering@market":[{"installPath":"PATH"}]}}`, required: true},
		{name: "installed", registry: `{"version":2,"plugins":{"compound-engineering@market":[{"installPath":"PATH"}]}}`, skill: true, required: true, wantOK: true},
		{name: "legacy record", registry: `{"version":1,"plugins":{"compound-engineering@market":{"installPath":"PATH"}}}`, skill: true, wantOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			config := filepath.Join(home, "custom config")
			plugin := filepath.Join(home, "plugin path")
			if tc.registry != "" {
				if err := os.MkdirAll(filepath.Join(config, "plugins"), 0755); err != nil {
					t.Fatal(err)
				}
				quoted, _ := json.Marshal(plugin)
				registry := strings.ReplaceAll(tc.registry, `"PATH"`, string(quoted))
				if err := os.WriteFile(filepath.Join(config, "plugins", "installed_plugins.json"), []byte(registry), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.skill {
				dir := filepath.Join(plugin, "skills", "ce-code-review")
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("fixture skill"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HOME", home)
			t.Setenv("CLAUDE_CONFIG_DIR", config)
			args := []string{"check-review-plugin.sh"}
			if tc.required {
				args = append(args, "--required")
			}
			out, err := exec.Command("bash", args...).CombinedOutput()
			if (err != nil) != (tc.required && !tc.wantOK) {
				t.Fatalf("unexpected status %v: %s", err, out)
			}
			if tc.wantOK {
				if !strings.Contains(string(out), "ce-code-review installed") {
					t.Fatalf("%s", out)
				}
			} else {
				for _, want := range []string{"Review prerequisite missing", "claude plugin marketplace add EveryInc/compound-engineering-plugin && claude plugin install compound-engineering", "restart the review session"} {
					if !strings.Contains(string(out), want) {
						t.Errorf("missing %q: %s", want, out)
					}
				}
			}
		})
	}
}

func TestReviewPluginWithoutJQWarns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	// Keep only the command needed to print guidance; jq is deliberately absent.
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	cat, err := exec.LookPath("cat")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cat, filepath.Join(bin, "cat")); err != nil {
		t.Fatal(err)
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	out, err := exec.Command(bash, "check-review-plugin.sh").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "jq is required to check") {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestInstallerReviewPrerequisiteIsWarnOnly(t *testing.T) {
	raw, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	// Run only the real installer's read-only prerequisites in a private checkout.
	// Stop before Agent Mail setup, build, installation or service operations.
	prefix, _, ok := strings.Cut(string(raw), "# ── Agent Mail:")
	if !ok {
		t.Fatal("missing prerequisite boundary")
	}
	repo := t.TempDir()
	dir := filepath.Join(repo, "install")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{"install.sh": []byte(prefix + "\necho PREREQUISITES_CONTINUED\n"), "check-review-plugin.sh": nil} {
		if body == nil {
			body, err = os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, name), body, 0644); err != nil {
			t.Fatal(err)
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	out, err := exec.Command("bash", filepath.Join(dir, "install.sh"), "--quiet").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "PREREQUISITES_CONTINUED") || !strings.Contains(string(out), "Review prerequisite missing") {
		t.Fatalf("%v: %s", err, out)
	}
}
