package evals

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The real cmdr and real checks on a throwaway repo with a private home: the
// queue, the build slot and the lock are all taken, only the merge is left out.
func TestShipDryRunScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/ship-dry-run.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Files    map[string]string            `json:"files"`
		Branches map[string]map[string]string `json:"branches"`
		Cases    []struct {
			Name    string   `json:"name"`
			Args    []string `json:"args"`
			Needs   string   `json:"needs"`
			Refused bool     `json:"refused"`
			Moves   bool     `json:"moves"`
			Output  []string `json:"output"`
			Absent  []string `json:"absent"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	binary := filepath.Join(base, "cmdr")
	mustBuild(t, os.Environ(), binary, "./cmd/cmdr")

	env := os.Environ()
	for _, name := range []string{"GOCACHE", "GOPATH"} { // a private HOME must not cost a cold build cache
		out, err := exec.Command("go", "env", name).Output()
		if err != nil {
			t.Fatal(err)
		}
		env = append(env, name+"="+strings.TrimSpace(string(out)))
	}
	home := filepath.Join(base, "home")
	env = append(env, "HOME="+base, "XDG_CACHE_HOME="+filepath.Join(base, "cache"), "COMMANDER_HOME="+home,
		"COMMANDER_BUILD_SLOTS=1", "COMMANDER_SHIP_MAX_THREADS=0")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(base, "demo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		cmd.Dir = repo
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(files map[string]string) {
		t.Helper()
		for name, text := range files {
			path := filepath.Join(repo, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		git("add", ".")
		git("commit", "-m", "scenario files")
	}
	git("init", "-b", "main")
	commit(sc.Files)
	for branch, files := range sc.Branches {
		git("checkout", "-b", branch, "main")
		commit(files)
	}
	git("checkout", "main")

	for _, tc := range sc.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			if tc.Needs != "" {
				if _, err := exec.LookPath(tc.Needs); err != nil {
					t.Skipf("no %s on PATH", tc.Needs)
				}
			}
			before := git("rev-parse", "main")
			cmd := exec.Command(binary, append([]string{"internal-ship"}, tc.Args...)...)
			cmd.Dir = repo
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			text := string(out)
			if tc.Refused != (err != nil) {
				t.Fatalf("refused=%v, want %v:\n%s", err != nil, tc.Refused, text)
			}
			for _, want := range tc.Output {
				if !strings.Contains(text, want) {
					t.Errorf("output misses %q:\n%s", want, text)
				}
			}
			for _, not := range tc.Absent {
				if strings.Contains(text, not) {
					t.Errorf("output has %q:\n%s", not, text)
				}
			}
			after := git("rev-parse", "main")
			if tc.Moves && after == before {
				t.Errorf("the real ship left main at %s", before)
			}
			if !tc.Moves && after != before {
				t.Errorf("main moved: %s -> %s", before, after)
			}
		})
	}
}
