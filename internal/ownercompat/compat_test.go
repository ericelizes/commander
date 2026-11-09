package ownercompat

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{legacyOwner: "user", LegacyNeedsLabel: "needs-user", "alice": "alice", "user": "user", "reviewer:alice": "reviewer:alice"} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestConfiguredAliasesCountAsUser(t *testing.T) {
	noThrottle(t)
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv(AliasesEnv, "")
	if got := Normalize("zed-old"); got != "zed-old" {
		t.Fatalf("unconfigured alias normalized to %q", got)
	}
	if err := os.WriteFile(filepath.Join(home, AliasesFile), []byte("# old names\nzed-old\n\n  quill-old # trailing\nuser\nreviewer:x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(AliasesEnv, "env-old, ")
	for in, want := range map[string]string{
		"zed-old": "user", "quill-old": "user", "env-old": "user",
		"needs-zed-old": "needs-user", "needs-env-old": "needs-user",
		"mallory": "mallory", "zed": "zed", "zed-older": "zed-older", "reviewer:x": "reviewer:x", "needs-mallory": "needs-mallory", "": "",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q)=%q, want %q", in, got, want)
		}
	}
}

func noThrottle(t testing.TB) {
	t.Setenv("COMMANDER_ROLE", "")
	t.Setenv("COMMANDER_MANAGER_ROLE", "")
	old := statEvery
	statEvery = 0
	t.Cleanup(func() { statEvery = old })
}

// The environment never adds an alias in an agent's process, and agent role
// names never become one from any source.
func TestEnvAliasIgnoredForAgentsAndRoleNamesRefused(t *testing.T) {
	noThrottle(t)
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv(AliasesEnv, "env-old,commander,manager-ms-1")
	t.Setenv("COMMANDER_ROLE", "")
	t.Setenv("COMMANDER_MANAGER_ROLE", "")
	if Normalize("env-old") != "user" {
		t.Fatal("the user's own process reads the env alias")
	}
	for _, v := range []string{"commander", "manager-ms-1"} {
		if Normalize(v) != v {
			t.Fatalf("role name %q became an alias", v)
		}
	}
	if err := os.WriteFile(filepath.Join(home, AliasesFile), []byte("file-old\nOperator-x\nmanager\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"COMMANDER_ROLE", "COMMANDER_MANAGER_ROLE"} {
		t.Run(role, func(t *testing.T) {
			t.Setenv(role, "manager-ms-1")
			if got := Normalize("env-old"); got != "env-old" {
				t.Fatalf("agent process read the env alias: %q", got)
			}
			if got := Normalize("file-old"); got != "user" {
				t.Fatalf("the user's file still counts for agents: %q", got)
			}
			for _, v := range []string{"Operator-x", "manager"} {
				if Normalize(v) != v {
					t.Fatalf("role name %q became an alias", v)
				}
			}
		})
	}
}

// The file is stat'ed at most once per statEvery and re-read only when it
// changed; sizes that once collided in the key are told apart.
func TestAliasFileCacheInvalidates(t *testing.T) {
	noThrottle(t)
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv(AliasesEnv, "")
	path := filepath.Join(home, AliasesFile)
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("aa-old\n")
	if Normalize("aa-old") != "user" {
		t.Fatal("first read")
	}
	write("bb-old\n") // same size
	mt := time.Unix(1700000000, 0)
	_ = os.Chtimes(path, mt, mt)
	if Normalize("bb-old") != "user" || Normalize("aa-old") == "user" {
		t.Fatal("a changed file must be re-read")
	}
	statEvery = time.Hour
	write("cc-old\n")
	if Normalize("cc-old") == "user" {
		t.Fatal("within statEvery the file is not stat'ed again")
	}
}

// Normalize runs per label per bead on every snapshot load.
func BenchmarkNormalize(b *testing.B) {
	home := b.TempDir()
	b.Setenv("COMMANDER_HOME", home)
	b.Setenv(AliasesEnv, "")
	if err := os.WriteFile(filepath.Join(home, AliasesFile), []byte("quill-old\n"), 0o600); err != nil {
		b.Fatal(err)
	}
	labels := []string{"needs-quill-old", "owner:manager-x", "quill-old", "kind:task"}
	for i := 0; i < 50; i++ {
		labels = append(labels, fmt.Sprintf("label-%d", i))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Normalize(labels[i%len(labels)])
	}
}
