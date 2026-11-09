package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/ship"
	"github.com/example-user/commander/internal/store"
)

func shipGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// shipRepo is a small Go module on main with a branch feat one commit ahead
// holding goSource, under a private home and cache; the test runs inside it.
func shipRepo(t *testing.T, goSource string) string {
	t.Helper()
	for _, name := range []string{"GOCACHE", "GOPATH"} {
		out, err := exec.Command("go", "env", name).Output()
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv(name, strings.TrimSpace(string(out))) // a private HOME must not cost a cold build cache
	}
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(base, "cache"))
	t.Setenv("COMMANDER_HOME", filepath.Join(base, "home"))
	t.Setenv("COMMANDER_BUILD_SLOTS", "1")
	t.Setenv("COMMANDER_SHIP_MAX_THREADS", "0")
	if err := os.MkdirAll(filepath.Join(base, "home"), 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, text string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		shipGit(t, dir, "add", name)
		shipGit(t, dir, "commit", "-m", name)
	}
	shipGit(t, dir, "init", "-b", "main")
	write("go.mod", "module example.test/demo\n\ngo 1.25\n")
	write("tests/evals/doc.go", "package evals\n") // the ship's eval check needs the folder
	shipGit(t, dir, "checkout", "-b", "feat")
	write("demo.go", goSource)
	shipGit(t, dir, "checkout", "main")
	t.Chdir(dir)
	return dir
}

const (
	shipGreen  = "package demo\n\nfunc Answer() int { return 1 }\n"
	shipBroken = "package demo\n\nfunc Answer() int { return missing }\n"
)

// runShipCLI runs the verb and returns its exit code and combined output.
func runShipCLI(t *testing.T, args ...string) (int, string) {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	code := runInternalShip(args, f, f)
	text, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return code, string(text)
}

// The real verb and real checks: a branch that does not build is refused for
// that reason, not for the flag, with main where it was.
func TestShipDryRunRefusesABrokenBranch(t *testing.T) {
	dir := shipRepo(t, shipBroken)
	main := shipGit(t, dir, "rev-parse", "main")
	code, out := runShipCLI(t, "feat", "--dry-run")
	if code == 0 {
		t.Fatalf("a broken branch passed:\n%s", out)
	}
	if !strings.Contains(out, "is red: go build") || !strings.Contains(out, "missing") {
		t.Errorf("want the failing build in the refusal:\n%s", out)
	}
	if !strings.Contains(out, "dry run") {
		t.Errorf("the refusal should say it was a dry run:\n%s", out)
	}
	if got := shipGit(t, dir, "rev-parse", "main"); got != main {
		t.Errorf("main moved: %s -> %s", main, got)
	}
}

func TestShipDryRunPassesAGreenBranchAndMovesNothing(t *testing.T) {
	dir := shipRepo(t, shipGreen)
	main, tip := shipGit(t, dir, "rev-parse", "main"), shipGit(t, dir, "rev-parse", "feat")
	code, out := runShipCLI(t, "--dry-run", "feat")
	if code != 0 {
		t.Fatalf("code %d:\n%s", code, out)
	}
	if !strings.Contains(out, "would move main") || !strings.Contains(out, "nothing merged") {
		t.Errorf("report:\n%s", out)
	}
	if got := shipGit(t, dir, "rev-parse", "main"); got != main {
		t.Errorf("main moved: %s -> %s", main, got)
	}
	if got := shipGit(t, dir, "rev-parse", "feat"); got != tip {
		t.Errorf("branch moved: %s -> %s", tip, got)
	}
}

// Wherever --dry-run sits it counts: Go's flag parser stops at the first
// positional, so it would otherwise read as a stray word and the ship go real.
func TestShipDryRunIsNeverIgnored(t *testing.T) {
	dir := shipRepo(t, shipGreen)
	main := shipGit(t, dir, "rev-parse", "main")
	for _, args := range [][]string{
		{"feat", "--dry-run"},
		{"--dry-run", "feat"},
		{"--dry-run=true", "feat"},
	} {
		if code, out := runShipCLI(t, args...); code != 0 || !strings.Contains(out, "would move main") {
			t.Errorf("%v: code %d:\n%s", args, code, out)
		}
		if got := shipGit(t, dir, "rev-parse", "main"); got != main {
			t.Fatalf("%v moved main", args)
		}
	}
	for _, args := range [][]string{
		{"feat", "stray", "--dry-run"},
		{"feat", "stray"},
		{"--dry-run", "feat", "stray"},
	} {
		code, out := runShipCLI(t, args...)
		if code == 0 || !strings.Contains(out, "usage:") {
			t.Errorf("%v: want a refusal with the usage, code %d:\n%s", args, code, out)
		}
		if got := shipGit(t, dir, "rev-parse", "main"); got != main {
			t.Fatalf("%v moved main", args)
		}
	}
	if code, out := runShipCLI(t, "--bad-flag", "feat"); code == 0 {
		t.Errorf("an unknown flag passed:\n%s", out)
	}
}

// `cmdr runtime report` counts operator-shipped events as landed work: a dry
// run writes none, and the real ship beside it shows the test can see one.
func TestShipDryRunRecordsNoShippedEvent(t *testing.T) {
	dir := shipRepo(t, shipGreen)
	worktree := filepath.Join(dir, ".worktrees", "op-demo")
	shipGit(t, dir, "worktree", "add", worktree, "feat")
	previous := newBeadsClientFn
	newBeadsClientFn = func() beads.Client { return &holdBeads{} }
	t.Cleanup(func() { newBeadsClientFn = previous })

	ctx := context.Background()
	path, err := stateDBPath()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	project, err := st.InsertProject(ctx, store.Project{Name: "demo", Type: "code", Path: dir, Status: "active", ActivatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertAgentRun(ctx, store.AgentRun{ProjectID: project, TaskBeadID: "demo-1", AgentName: "operator-op-demo", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	shipped := func() int {
		events, err := st.ListCostEventsSince(ctx, 0, store.EventOperatorShipped)
		if err != nil {
			t.Fatal(err)
		}
		return len(events)
	}
	if code, out := runShipCLI(t, "feat", "--dry-run"); code != 0 {
		t.Fatalf("dry run: code %d:\n%s", code, out)
	}
	if n := shipped(); n != 0 {
		t.Errorf("a dry run recorded %d shipped events", n)
	}
	if code, out := runShipCLI(t, "feat"); code != 0 {
		t.Fatalf("real ship: code %d:\n%s", code, out)
	}
	if n := shipped(); n != 1 {
		t.Errorf("the real ship recorded %d shipped events, want 1", n)
	}
}

// A wait that ran out says nothing about the branch: the line must not read as
// a verdict, or as the refusal a real ship would give.
func TestShipDryRunExpiredQueueIsNoVerdict(t *testing.T) {
	dir := shipRepo(t, shipGreen)
	t.Setenv("COMMANDER_SHIP_QUEUE_WAIT", "300ms")
	release, err := ship.HoldVerifyLock(context.Background(), commanderHomeDir(), dir, io.Discard, func(context.Context, io.Writer) (func(), error) { return func() {}, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	main := shipGit(t, dir, "rev-parse", "main")
	code, out := runShipCLI(t, "feat", "--dry-run")
	if code == 0 {
		t.Fatalf("an expired wait passed:\n%s", out)
	}
	if !strings.Contains(out, "no verdict on the branch") || strings.Contains(out, "would refuse") {
		t.Errorf("want no verdict:\n%s", out)
	}
	if got := shipGit(t, dir, "rev-parse", "main"); got != main {
		t.Errorf("main moved: %s -> %s", main, got)
	}
}

func TestShipDryRunLabel(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{errors.New("main checkout is dirty"), "a real ship would refuse"},
		{ship.Inconclusive{Err: errors.New("terminated during go test")}, "no verdict"},
		{fmt.Errorf("wrapped: %w", ship.Inconclusive{Err: errors.New("x")}), "no verdict"},
	} {
		if got := dryRunLabel(c.err); !strings.Contains(got, c.want) {
			t.Errorf("%v: label %q, want %q", c.err, got, c.want)
		}
	}
}
