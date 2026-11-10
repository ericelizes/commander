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
		Files     map[string]string               `json:"files"`
		Branches  map[string]map[string]string    `json:"branches"`
		Histories map[string][]map[string]*string `json:"histories"`
		Cases     []struct {
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
	// A stand-in gitleaks: red when a commit in --log-opts adds the fake key,
	// unless "fake-key" is listed in the --gitleaks-ignore-path file or in the
	// --source tree's .gitleaksignore (real gitleaks reads both); a line with
	// an inline gitleaks:allow passes unless --ignore-gitleaks-allow is given.
	bin := filepath.Join(base, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := `#!/bin/sh
src=. range= ignore=/dev/null allow=gitleaks:allow
while [ $# -gt 0 ]; do
  case "$1" in
    --ignore-gitleaks-allow) allow=no-inline-allow-is-honoured ;;
    --source) src=$2; shift ;;
    --log-opts) range=$2; shift ;;
    --gitleaks-ignore-path) ignore=$2; shift ;;
  esac
  shift
done
echo '1:00AM INF scan completed in 1ms'
patch=$(git -C "$src" log -p $range | grep -v "$allow")
if ! printf '%s\n' "$patch" | grep -q AKIAFAKE; then
  # A multiline stand-in only matches a complete added block in one patch.
  printf '%s\n' "$patch" | awk '/^diff --git/ {start=0} /^\+BEGIN-INVENTED-KEY/ {start=1} /^\+END-INVENTED-KEY/ {if (start) found=1} END {exit !found}' || exit 0
fi
if grep -qx fake-key "$ignore" 2>/dev/null || grep -qx fake-key "$src/.gitleaksignore" 2>/dev/null; then exit 0; fi
echo 'Finding: fake-key'
echo '1:00AM WRN leaks found: 1'
exit 1
`
	if err := os.WriteFile(filepath.Join(bin, "gitleaks"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	env = append(env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
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
	for branch, history := range sc.Histories {
		git("checkout", "-b", branch, "main")
		for _, files := range history {
			for name, content := range files {
				path := filepath.Join(repo, name)
				if content == nil {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(path, []byte(*content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			git("add", ".")
			git("commit", "-qm", "invented intermediate snapshot")
		}
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
