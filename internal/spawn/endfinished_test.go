package spawn

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/example-user/commander/internal/docker"
	"github.com/example-user/commander/internal/testguard"
	"github.com/example-user/commander/internal/tmuxctl"
)

// An Operator still running after its final report (a background poll loop keeps claude alive) is
// ended by cmdrd. With the real launcher on a private tmux server: a bare pane kill HUPs the
// launcher before it writes its exit marker, so the reaper reads a death (the bug, M1);
// EndFinishedSession leaves the marker first, so the reaper reads the run as exited, which the
// settle hands to the Manager with its report.
func TestEndFinishedSession_RealLauncherReadsAsExited(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	for _, tc := range []struct {
		name   string
		end    func(ctx context.Context, session string) error
		exited bool
	}{
		{"bare pane kill", func(ctx context.Context, session string) error {
			return tmuxctl.New().KillPane(ctx, tmuxctl.ExactPane(session))
		}, false},
		{"EndFinishedSession", func(ctx context.Context, session string) error {
			return EndFinishedSession(ctx, session, nil)
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := makeGitRepo(t)
			testguard.NoLeakedProcesses(t, dir)
			sp, fs, _, _, _, _ := newHostSpawner(t, dir)
			socket := privateOpsServer(t, "#!/bin/sh\necho '{\"type\":\"system\"}'\nsleep 60\n")
			sp.tmux = tmuxctl.New()

			req := goodRequest(t, dir)
			req.ProjectType = "code"
			if _, err := sp.Spawn(context.Background(), req); err != nil {
				t.Fatalf("Spawn: %v", err)
			}
			session := hostSessionName(req.ProjectName, req.slug())
			alive := func() bool { return exec.Command("tmux", "-L", socket, "has-session", "-t", "="+session).Run() == nil }
			if !alive() {
				t.Fatal("the Operator's session is not running")
			}
			time.Sleep(300 * time.Millisecond) // the launcher is running the agent

			if err := tc.end(context.Background(), session); err != nil {
				t.Fatalf("end: %v", err)
			}
			waitFor(t, 10*time.Second, func() bool { return !alive() })

			fs.mu.Lock()
			runs := fs.runs
			fs.mu.Unlock()
			if len(runs) != 1 {
				t.Fatalf("runs %v", runs)
			}
			r := NewReaper(fs, &docker.MockClient{}).WithHost(sp.tmux, hostRunLister{runs: runs})
			r.exited = exitedSince
			res, err := r.ReapHost(context.Background())
			if err != nil {
				t.Fatalf("ReapHost: %v", err)
			}
			if got := len(res.Exited) == 1 && len(res.Died) == 0; got != tc.exited {
				t.Fatalf("exited %v died %v, want exited=%v", res.Exited, res.Died, tc.exited)
			}
			want := "orphan-reaped"
			if tc.exited {
				want = "exited"
			}
			if fs.closed[runs[0].ID] != want {
				t.Fatalf("closed %v, want %s", fs.closed, want)
			}
		})
	}
}
