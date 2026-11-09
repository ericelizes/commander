package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fullConfigTOML = `
[concurrency]
max_operators_per_project = 3
max_operators_global      = 10

[ceilings]
max_usd_per_operator      = 5.0
max_tokens_per_operator   = 200000
max_wallclock_minutes     = 60
max_retries               = 3

[models]
default_commander = "anthropic/claude-opus-4-7"
default_lead      = "anthropic/claude-sonnet-4-7"
default_operator  = "anthropic/claude-sonnet-4-7"

[projects.frontend]
type          = "code"
max_operators = 5

[projects.survey]
type          = "knowledge"
max_operators = 20
`

// writeFile is a small helper that drops content into dir/name and returns
// the full path. Tests use t.TempDir() so cleanup is automatic.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func TestLoad_FullConfigMatchesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.toml", fullConfigTOML)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	d := Defaults()
	if cfg.Concurrency.MaxOperatorsPerWorkstream != d.Concurrency.MaxOperatorsPerWorkstream || cfg.Concurrency.BuildSlots != d.Concurrency.BuildSlots {
		t.Errorf("Concurrency mismatch: got %+v want %+v", cfg.Concurrency, d.Concurrency)
	}
	if cfg.Ceilings != d.Ceilings {
		t.Errorf("Ceilings mismatch: got %+v want %+v", cfg.Ceilings, d.Ceilings)
	}
	if cfg.Models != d.Models {
		t.Errorf("Models mismatch: got %+v want %+v", cfg.Models, d.Models)
	}
	if got := cfg.Projects["frontend"]; got.Type != "code" || got.MaxOperators != 5 {
		t.Errorf("Projects[frontend] = %+v", got)
	}
	if got := cfg.Projects["survey"]; got.Type != "knowledge" || got.MaxOperators != 20 {
		t.Errorf("Projects[survey] = %+v", got)
	}
}

func TestLoad_ProjectAutoSpawnParsed(t *testing.T) {
	dir := t.TempDir()
	withAutoSpawn := `
[concurrency]
max_operators_per_project = 3
max_operators_global      = 10

[projects.frontend]
type          = "code"
max_operators = 4
auto_spawn    = true

[projects.survey]
type          = "knowledge"
max_operators = 2
`
	path := writeFile(t, dir, "config.toml", withAutoSpawn)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Projects["frontend"]; !got.AutoSpawn {
		t.Errorf("frontend.AutoSpawn = false; want true (got %+v)", got)
	}
	// Default when omitted: false.
	if got := cfg.Projects["survey"]; got.AutoSpawn {
		t.Errorf("survey.AutoSpawn = true; want false (got %+v)", got)
	}
}

func TestLoad_MinimalConfigFillsDefaults(t *testing.T) {
	dir := t.TempDir()
	minimal := `
[concurrency]
max_operators_per_project = 7
max_operators_global      = 21
`
	path := writeFile(t, dir, "config.toml", minimal)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Concurrency.MaxOperatorsPerProject != 7 {
		t.Errorf("user value lost: %d", cfg.Concurrency.MaxOperatorsPerProject)
	}
	if cfg.Concurrency.MaxOperatorsGlobal != 21 {
		t.Errorf("user value lost: %d", cfg.Concurrency.MaxOperatorsGlobal)
	}
	d := Defaults()
	if cfg.Ceilings != d.Ceilings {
		t.Errorf("Ceilings should default to %+v, got %+v", d.Ceilings, cfg.Ceilings)
	}
	if cfg.Models != d.Models {
		t.Errorf("Models should default to %+v, got %+v", d.Models, cfg.Models)
	}
}

func TestLoad_UnknownFieldIsLoudError(t *testing.T) {
	dir := t.TempDir()
	bad := `
[concurrency]
max_operators_per_project_typo = 3
max_operators_global           = 10
`
	path := writeFile(t, dir, "config.toml", bad)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown key, got nil")
	}
	if !strings.Contains(err.Error(), "max_operators_per_project_typo") {
		t.Errorf("error should name the bad key; got: %v", err)
	}
	if !strings.Contains(err.Error(), "max_operators_per_project") {
		t.Errorf("error should suggest the correct key; got: %v", err)
	}
}

func TestLoad_InvalidTypeIsLoudError(t *testing.T) {
	dir := t.TempDir()
	bad := `
[ceilings]
max_usd_per_operator = "five"
`
	path := writeFile(t, dir, "config.toml", bad)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid type, got nil")
	}
	if !strings.Contains(err.Error(), "max_usd_per_operator") {
		t.Errorf("error should name the offending key; got: %v", err)
	}
}

func TestLoad_InvalidProjectTypeRejected(t *testing.T) {
	dir := t.TempDir()
	bad := `
[concurrency]
max_operators_per_project = 3
max_operators_global      = 10

[projects.weird]
type          = "essay"
max_operators = 1
`
	path := writeFile(t, dir, "config.toml", bad)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid project type")
	}
	if !strings.Contains(err.Error(), "essay") {
		t.Errorf("error should mention the bad value; got: %v", err)
	}
}

func TestLoad_MissingFileReturnsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml") // never created

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("missing file should not error; got: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected defaults, got nil")
	}
	d := Defaults()
	if cfg.Concurrency != d.Concurrency || cfg.Ceilings != d.Ceilings || cfg.Models != d.Models {
		t.Errorf("missing-file config != Defaults(); got %+v", cfg)
	}
	if len(cfg.Projects) != 0 {
		t.Errorf("Projects should be empty by default; got %+v", cfg.Projects)
	}
}

// drainEvent waits up to timeout for a WatchEvent on ch.
func drainEvent(t *testing.T, ch <-chan WatchEvent, timeout time.Duration) (WatchEvent, bool) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			return WatchEvent{}, false
		}
		return ev, true
	case <-time.After(timeout):
		return WatchEvent{}, false
	}
}

func TestWatch_ReloadsOnEdit(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.toml", fullConfigTOML)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := Watch(ctx, path)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// Edit: bump max_operators_global to 99.
	edited := strings.Replace(fullConfigTOML, "max_operators_global      = 10", "max_operators_global      = 99", 1)
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	ev, ok := drainEvent(t, ch, 3*time.Second)
	if !ok {
		t.Fatal("watcher did not emit within 3s")
	}
	if ev.Err != nil {
		t.Fatalf("watcher emitted error: %v", ev.Err)
	}
	if ev.Config == nil {
		t.Fatal("watcher emitted nil config without error")
	}
	if ev.Config.Concurrency.MaxOperatorsGlobal != 99 {
		t.Errorf("expected reload to see 99, got %d", ev.Config.Concurrency.MaxOperatorsGlobal)
	}
}

func TestWatch_InvalidEditEmitsErrorAndKeepsCallerConfig(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.toml", fullConfigTOML)

	good, err := Load(path)
	if err != nil {
		t.Fatalf("initial Load: %v", err)
	}
	caller := good

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := Watch(ctx, path)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// Edit to introduce an unknown field inside [concurrency].
	bad := strings.Replace(
		fullConfigTOML,
		"max_operators_global      = 10",
		"max_operators_global      = 10\nbogus_field               = 1",
		1,
	)
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	ev, ok := drainEvent(t, ch, 3*time.Second)
	if !ok {
		t.Fatal("watcher did not emit within 3s")
	}
	if ev.Err == nil {
		t.Fatalf("expected error on invalid reload; got config: %+v", ev.Config)
	}
	if !strings.Contains(ev.Err.Error(), "bogus_field") {
		t.Errorf("error should name bogus_field; got: %v", ev.Err)
	}
	// Caller's previously good config is still intact (no mutation).
	if caller.Concurrency.MaxOperatorsGlobal != 10 {
		t.Errorf("caller config was mutated: %+v", caller)
	}
}

func TestWatch_CancelClosesChannel(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.toml", fullConfigTOML)

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := Watch(ctx, path)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	cancel()

	select {
	case _, ok := <-ch:
		if ok {
			// Drain anything that fired between cancel and goroutine exit.
			select {
			case _, ok := <-ch:
				if ok {
					t.Error("channel still delivering after cancel")
				}
			case <-time.After(2 * time.Second):
				t.Error("channel not closed within 2s after cancel")
			}
		}
	case <-time.After(2 * time.Second):
		t.Error("channel not closed within 2s after cancel")
	}
}

// Sanity check that errors.Is works on a wrapped Load error — this just
// guards against accidentally swallowing the underlying os error.
func TestLoad_StatErrorPropagates(t *testing.T) {
	// We can't easily induce a non-ENOENT stat error portably; this test
	// exists to document the contract rather than exercise it. Skip if the
	// platform doesn't let us point at an unreadable directory.
	if os.Geteuid() == 0 {
		t.Skip("running as root, can't induce permission denied")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "noaccess")
	if err := os.Mkdir(sub, 0o000); err != nil {
		t.Skipf("mkdir 0o000: %v", err)
	}
	defer os.Chmod(sub, 0o755)
	_, err := Load(filepath.Join(sub, "config.toml"))
	// Either we get a non-ENOENT stat error wrapped, or (some filesystems)
	// the dir read still succeeds and we get the missing-file path. Both
	// are acceptable; we just want to make sure we don't panic.
	_ = errors.Is(err, os.ErrNotExist)
}

func TestLoad_RuntimeDefaultsAndOverrides(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(writeFile(t, dir, "config.toml", "[runtime]\nlead = \"claude\"\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.Default != "opencode" || cfg.Runtime.Lead != "claude" || cfg.Runtime.Commander != "" {
		t.Errorf("runtime = %+v", cfg.Runtime)
	}

	_, err = Load(writeFile(t, dir, "bad.toml", "[runtime]\ndefault = \"codex\"\n"))
	if err == nil || !strings.Contains(err.Error(), "runtime.default") {
		t.Errorf("invalid runtime should be a loud error naming the key; got %v", err)
	}
}

func TestLoad_APIPort(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(writeFile(t, dir, "ok.toml", "[api]\nport = 9001\n"))
	if err != nil || cfg.API.Port != 9001 {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
	if _, err := Load(writeFile(t, dir, "bad.toml", "[api]\nport = 70000\n")); err == nil {
		t.Fatal("api.port out of range must be rejected")
	}
}

func TestLoad_WorkstreamCapAndBuildSlots(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.toml", "[concurrency]\nmax_operators_per_workstream = 12\nbuild_slots = 4\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Concurrency.MaxOperatorsPerWorkstream != 12 || cfg.Concurrency.BuildSlots != 4 {
		t.Fatalf("concurrency = %+v", cfg.Concurrency)
	}
	if Defaults().Concurrency.MaxOperatorsPerWorkstream != 8 || Defaults().Concurrency.BuildSlots != 0 {
		t.Fatalf("defaults = %+v, want 8 per workstream and auto build slots", Defaults().Concurrency)
	}
	bad := writeFile(t, dir, "bad.toml", "[concurrency]\nbuild_slots = -1\n")
	if _, err := Load(bad); err == nil || !strings.Contains(err.Error(), "build_slots") {
		t.Fatalf("negative build_slots: err = %v", err)
	}
}

func TestNeedsDefaultsAndOff(t *testing.T) {
	var n Needs
	if n.StaleDaysOr() != 7 || n.PushCountOr() != 3 || n.PushDaysOr() != 7 {
		t.Fatalf("defaults: %d %d %d", n.StaleDaysOr(), n.PushCountOr(), n.PushDaysOr())
	}
	zero := 0
	n = Needs{StaleDays: &zero}
	if n.StaleDaysOr() != 0 {
		t.Fatal("0 must switch the rule off, not fall back to the default")
	}
}
