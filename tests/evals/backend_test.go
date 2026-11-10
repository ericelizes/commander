package evals

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Capture before testguard/harness HOME isolation. Every entrypoint, including
// direct go test and ship's clean verification tree, uses this same cache.
var evalBackendCache, evalBackendCacheErr = os.UserCacheDir()

func resolveEvalBR(override, cache string) (string, error) {
	if override != "" {
		return override, nil
	}
	manifest := filepath.Join(cache, "commander", "eval-tools", "backend.json")
	body, err := os.ReadFile(manifest)
	if err != nil {
		return "", fmt.Errorf("eval backend unavailable: run python3 scripts/setup-eval-backend.py /absolute/path/to/trusted/beads_rust: %w", err)
	}
	var record struct{ Schema, Path, Version, SHA256 string }
	if err := json.Unmarshal(body, &record); err != nil {
		return "", err
	}
	if record.Schema != "eval-backend@v1" || record.Version != "br 0.7.6" || !filepath.IsAbs(record.Path) {
		return "", fmt.Errorf("invalid eval backend manifest %s", manifest)
	}
	binary, err := os.ReadFile(record.Path)
	if err != nil {
		return "", err
	}
	if fmt.Sprintf("%x", sha256.Sum256(binary)) != record.SHA256 {
		return "", fmt.Errorf("eval backend SHA-256 mismatch: %s", record.Path)
	}
	return record.Path, nil
}

// installEvalBR deliberately never searches PATH for br: a host br can be a
// production routing fence with a completely different on-disk format.
// The caller supplies either the registered private copy or an explicit override.
func installEvalBR(binDir, backend string) error {
	if !filepath.IsAbs(backend) {
		return fmt.Errorf("eval requires COMMANDER_EVAL_BR_BIN: absolute path to beads_rust (not the host br wrapper)")
	}
	info, err := os.Stat(backend)
	if err != nil {
		return fmt.Errorf("eval beads_rust dependency %q unavailable: %w", backend, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("eval beads_rust dependency %q is not executable", backend)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, backend, "--version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(version)) != "br 0.7.6" {
		return fmt.Errorf("eval beads_rust dependency %q: expected br 0.7.6, got %q (%v)", backend, version, err)
	}
	body, err := os.ReadFile(backend)
	if err != nil {
		return err
	}
	// Record the actual dependency identity in verbose/JSON test output.
	fmt.Printf("eval beads_rust: path=%s version=%s sha256=%x\n", backend, strings.TrimSpace(string(version)), sha256.Sum256(body))
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	return os.WriteFile(filepath.Join(binDir, "br"), []byte("#!/bin/sh\nexec "+quote(backend)+" \"$@\"\n"), 0700)
}

func TestEvalBRSelection(t *testing.T) {
	dir := t.TempDir()
	host := filepath.Join(dir, "host")
	private := filepath.Join(dir, "private")
	for _, d := range []string{host, private} {
		if err := os.Mkdir(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable(t, filepath.Join(host, "br"), "#!/bin/sh\necho host-fence >&2\nexit 91\n")
	backend := filepath.Join(dir, "rust backend's executable")
	writeExecutable(t, backend, "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'br 0.7.6'; else printf 'rust:%s:%s\\n' \"$1\" \"$2\"; fi\n")
	t.Setenv("PATH", private+":"+host)
	if err := installEvalBR(private, backend); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("br", "init", "two words").CombinedOutput()
	if err != nil || string(out) != "rust:init:two words\n" {
		t.Fatalf("private backend: %q %v", out, err)
	}
	for _, path := range []string{"", "br", filepath.Join(dir, "missing")} {
		if err := installEvalBR(private, path); err == nil {
			t.Fatalf("accepted unavailable dependency %q", path)
		}
	}
	writeExecutable(t, backend, "#!/bin/sh\necho 'bd 1.3.1'\n")
	if err := installEvalBR(private, backend); err == nil {
		t.Fatal("accepted wrong backend identity")
	}
}

// Exercise the real setup entrypoint with no backend environment override, as
// direct Go, Justfile and ship callers do. A host br must never be consulted.
func TestEvalBRDefaultEntrypoint(t *testing.T) {
	home := t.TempDir()
	cache := filepath.Join(home, "cache with apostrophe's")
	if runtime.GOOS == "darwin" {
		cache = filepath.Join(home, "Library", "Caches")
	}
	source := filepath.Join(t.TempDir(), "trusted backend's executable")
	writeExecutable(t, source, "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'br 0.7.6'; else printf 'private:%s\\n' \"$1\"; fi\n")
	setup := exec.Command("python3", filepath.Join(repoRoot(), "scripts", "setup-eval-backend.py"), source)
	setup.Env = append(os.Environ(), "HOME="+home, "XDG_CACHE_HOME="+cache, "COMMANDER_EVAL_BR_BIN=")
	if out, err := setup.CombinedOutput(); err != nil {
		t.Fatalf("setup: %s: %v", out, err)
	}
	// The provisioned copy remains usable independently of the selected source.
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	backend, err := resolveEvalBR("", cache)
	if err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	if err := installEvalBR(private, backend); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(filepath.Join(private, "br"), "two words").CombinedOutput()
	if err != nil || string(out) != "private:two words\n" {
		t.Fatalf("default backend: %q %v", out, err)
	}
	// Bytes changed after registration fail closed, before any version execution.
	writeExecutable(t, backend, "#!/bin/sh\necho 'br 0.7.6'\n")
	if _, err := resolveEvalBR("", cache); err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("corrupt backend accepted: %v", err)
	}
	if err := os.Remove(backend); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveEvalBR("", cache); err == nil {
		t.Fatal("missing registered binary accepted")
	}
}

func TestEvalBRUnavailableDefault(t *testing.T) {
	host := t.TempDir()
	marker := filepath.Join(host, "called")
	writeExecutable(t, filepath.Join(host, "br"), "#!/bin/sh\nprintf called > '"+marker+"'\nexit 91\n")
	t.Setenv("PATH", host)
	if _, err := resolveEvalBR("", t.TempDir()); err == nil || !strings.Contains(err.Error(), "setup-eval-backend.py") {
		t.Fatalf("missing dependency: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("host br was consulted")
	}
}
