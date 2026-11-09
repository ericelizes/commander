package daemon

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/docker"
	"github.com/example-user/commander/internal/opencode"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tmuxctl"
)

// bareNameAm is the am CLI as the server sees it: a bare project name is not a
// project ("Project 'commander' not found"); only the absolute repo path is.
type bareNameAm struct{ agentmail.MockClient }

func (a *bareNameAm) Register(ctx context.Context, project, program, model, name string) error {
	_ = a.MockClient.Register(ctx, project, program, model, name)
	if !filepath.IsAbs(project) {
		return errors.New("Project '" + project + "' not found")
	}
	return nil
}

type pathOnlyServer struct{ ensured, registered []string }

func (s *pathOnlyServer) EnsureProject(_ context.Context, key string) (string, error) {
	if !filepath.IsAbs(key) {
		return "", errors.New("not a path")
	}
	s.ensured = append(s.ensured, key)
	return "slug", nil
}
func (s *pathOnlyServer) RegisterAgent(_ context.Context, key, _, _, _ string) (string, error) {
	if !filepath.IsAbs(key) {
		return "", errors.New("not a path")
	}
	s.registered = append(s.registered, key)
	return "GreenLake", nil
}

type memNames map[string]string

func (m memNames) GetAmAgent(_ context.Context, key, role string) (string, error) {
	return m[key+"|"+role], nil
}
func (m memNames) UpsertAmAgent(_ context.Context, key, role, name string) error {
	m[key+"|"+role] = name
	return nil
}

// A crash restart registers through cmdrd's spawner with the bare project name
// (settle.go builds SpawnRequest{ProjectName: p.Name}); it must reuse the
// Operator's mapping or register under the repo path, never ask the CLI.
func TestSpawnerMail_RestartReusesTheRegistrationUnderThePathKey(t *testing.T) {
	ctx := context.Background()
	cli := &bareNameAm{}
	srv := &pathOnlyServer{}
	names := memNames{"/repo|operator-op-a": "OldName"}
	am := spawnerMail(cli, srv, names, "/repo")

	if err := am.Register(ctx, "commander", "claude-code", "opus", "operator-op-a"); err != nil {
		t.Fatalf("restart of a mapped Operator: %v", err)
	}
	if len(srv.ensured)+len(srv.registered)+len(cli.Calls) != 0 {
		t.Fatalf("mapped Operator registered again: %v %v %v", srv.ensured, srv.registered, cli.Calls)
	}
	if err := am.Register(ctx, "commander", "claude-code", "opus", "operator-op-b"); err != nil {
		t.Fatalf("new Operator: %v", err)
	}
	if len(cli.Calls) != 0 || len(srv.registered) != 1 || srv.registered[0] != "/repo" || names["/repo|operator-op-b"] != "GreenLake" {
		t.Fatalf("want one server registration under /repo, got cli %v server %v names %v", cli.Calls, srv.registered, names)
	}
}

func TestSpawnerMail_NoServerKeepsTheCLI(t *testing.T) {
	cli := &bareNameAm{}
	if got := spawnerMail(cli, nil, memNames{}, "/repo"); got != agentmail.Client(cli) {
		t.Fatalf("without a server token the CLI client stays, got %T", got)
	}
}

// cmdrd's real spawner, on a real restart request with a dotted role (as settle
// builds it): the stored spelling is reused, a new one registers under the repo
// path, and the am CLI's bare name is never asked. Passing the CLI client to the
// spawner again fails here: every restart then died at register.
func TestDaemonSpawner_DottedRestartRegistersThroughTheServer(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "t"}, {"commit", "--allow-empty", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	projectID, err := st.InsertProject(ctx, store.Project{Name: "commander", Type: "code", Path: repo, ActivatedAt: 1, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	const mapped, fresh = "op-x-commander-il9.153.4", "op-y-commander-il9.153.5"
	if err := st.UpsertAmAgent(ctx, repo, "operator-"+mapped, "OldName"); err != nil {
		t.Fatal(err)
	}
	cli, srv := &bareNameAm{}, &pathOnlyServer{}
	sp := newDaemonSpawner(st, &opencode.MockClient{}, &beads.MockClient{}, cli, srv, &docker.MockClient{}, &tmuxctl.MockClient{}, repo)
	for _, name := range []string{mapped, fresh} {
		if _, err := sp.Spawn(ctx, spawn.SpawnRequest{
			ProjectID: projectID, ProjectName: "commander", ProjectPath: repo, ProjectType: "code",
			TaskBeadID: "commander-il9.153.9", Branch: "op/" + name, Name: name, Model: "anthropic/test", Resume: true,
		}); err != nil {
			t.Fatalf("restart of %s: %v", name, err)
		}
	}
	if len(cli.Calls) != 0 {
		t.Fatalf("the am CLI was asked: %v", cli.Calls)
	}
	if len(srv.registered) != 1 || srv.registered[0] != repo {
		t.Fatalf("server registrations %v, want only the fresh role under %s", srv.registered, repo)
	}
	if got, _ := st.GetAmAgent(ctx, repo, "operator-"+mapped); got != "OldName" {
		t.Fatalf("mapped role now %q", got)
	}
	if got, _ := st.GetAmAgent(ctx, repo, "operator-"+fresh); got != "GreenLake" {
		t.Fatalf("fresh dotted role mapped to %q", got)
	}
}
