package evals

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// installEvalBR deliberately never searches PATH for br: a host br can be a
// production routing fence with a completely different on-disk format.
// COMMANDER_EVAL_BR_BIN must name the intended beads_rust executable explicitly.
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
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(version)), "br ") {
		return fmt.Errorf("eval beads_rust dependency %q: expected br version, got %q (%v)", backend, version, err)
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
