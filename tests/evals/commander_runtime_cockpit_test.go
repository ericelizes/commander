package evals

import (
	"context"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real detached cockpit, private PostgreSQL and fake model binaries. This
// exercises the actual startup/text interface without touching the user's
// monitor, tmux server, state, accounts or model subscriptions.
func TestCommanderRuntimeContractInIsolatedPostgresCockpit(t *testing.T) {
	r := newBareCmdrRig(t)
	st, dsn := pgtest.Open(t)
	ctx := context.Background()
	iso, socket := filepath.Join(r.dir, "pg-home"), filepath.Join(r.dir, "pg-socket")
	ch := filepath.Join(iso, "ch")
	project := filepath.Join(r.dir, "project")
	for _, dir := range []string{ch, filepath.Join(project, ".beads")} {
		if e := os.MkdirAll(dir, 0700); e != nil {
			t.Fatal(e)
		}
	}
	for path, value := range map[string]string{filepath.Join(iso, ".cockpit-isolated"): "private test fixture", filepath.Join(ch, "state.postgres"): dsn} {
		if e := os.WriteFile(path, []byte(value), 0600); e != nil {
			t.Fatal(e)
		}
	}
	pid, e := st.InsertProject(ctx, store.Project{Name: "runtime-cockpit", Path: project, Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,parent_id,title,description,acceptance_criteria,labels,execution_ready,created_at,updated_at) VALUES(?,'root',NULL,'Runtime result','','','["kind:objective","kind:workstream"]',FALSE,'now','now'),(?,'task','root','Bounded execution','Use the assigned scope','Record evidence','["kind:objective"]',TRUE,'now','now')`, pid, pid); e != nil {
		t.Fatal(e)
	}
	rid, e := st.InsertAgentRun(ctx, store.AgentRun{ProjectID: pid, TaskBeadID: "task", AgentName: "operator-cockpit-finished", OpencodeSessionID: "private-cockpit-session", SpawnTime: time.Now().Unix()})
	if e != nil {
		t.Fatal(e)
	}
	if e = st.FinishNativeExecutionReport(ctx, "operator-cockpit-finished", rid, "private-cockpit-session", "exited", "Prepared isolated result; awaiting planning review"); e != nil {
		t.Fatal(e)
	}
	if _, e = st.SetOrchestrationControl(ctx, 1, "active", "isolated runtime cockpit"); e != nil {
		t.Fatal(e)
	}
	script := func(verb string) (string, error) {
		deadline, cancel := context.WithTimeout(context.Background(), evalTimeout(t, 60*time.Second))
		defer cancel()
		command := exec.CommandContext(deadline, "bash", filepath.Join(repoRoot(), "scripts", "cockpit-test.sh"), verb)
		command.Env = []string{"HOME=" + filepath.Join(r.dir, "home"), "PATH=/usr/bin:/bin", "COCKPIT_HOME=" + iso, "COCKPIT_TMUX_TMPDIR=" + socket, "COCKPIT_CMDR=" + filepath.Join(r.bin, "cmdr"), "COCKPIT_FAKE_OPENCODE=" + filepath.Join(r.bin, "opencode")}
		command.WaitDelay = 3 * time.Second
		out, e := command.CombinedOutput()
		return string(out), e
	}
	t.Cleanup(func() {
		if out, e := script("down"); e != nil {
			t.Logf("isolated teardown: %v %s", e, out)
		}
	})
	if out, e := script("up"); e != nil {
		t.Fatalf("isolated startup: %v\n%s", e, out)
	}
	// Zoom only this private pane: the assertion must inspect full status text,
	// independently of the adaptive sidebar width of the headless layout.
	list := exec.CommandContext(ctx, r.tmux, "-S", filepath.Join(socket, "cockpit.sock"), "list-panes", "-t", "commander", "-F", "#{pane_id}|#{pane_start_command}")
	panes, e := list.Output()
	if e != nil {
		t.Fatal(e)
	}
	objectivesPane := ""
	zoomed := false
	for _, pane := range strings.Split(string(panes), "\n") {
		if strings.Contains(pane, "__column cmdr-objectives") {
			id := strings.SplitN(pane, "|", 2)[0]
			objectivesPane = id
			flag, _ := exec.CommandContext(ctx, r.tmux, "-S", filepath.Join(socket, "cockpit.sock"), "display-message", "-p", "-t", id, "#{window_zoomed_flag}").Output()
			if strings.TrimSpace(string(flag)) == "1" {
				command := exec.CommandContext(ctx, r.tmux, "-S", filepath.Join(socket, "cockpit.sock"), "resize-pane", "-Z", "-t", id)
				if out, e := command.CombinedOutput(); e != nil {
					t.Fatalf("private sidebar unzoom: %v %s", e, out)
				}
			}
			command := exec.CommandContext(ctx, r.tmux, "-S", filepath.Join(socket, "cockpit.sock"), "resize-pane", "-Z", "-t", id)
			if out, e := command.CombinedOutput(); e != nil {
				t.Fatalf("private sidebar zoom: %v %s", e, out)
			}
			zoomed = true
			break
		}
	}
	if !zoomed {
		t.Fatal("isolated objectives pane missing")
	}

	var text string
	deadline := time.Now().Add(evalTimeout(t, 20*time.Second))
	for time.Now().Before(deadline) {
		flag, _ := exec.CommandContext(ctx, r.tmux, "-S", filepath.Join(socket, "cockpit.sock"), "display-message", "-p", "-t", objectivesPane, "#{window_zoomed_flag}").Output()
		if strings.TrimSpace(string(flag)) != "1" {
			out, zoomErr := exec.CommandContext(ctx, r.tmux, "-S", filepath.Join(socket, "cockpit.sock"), "resize-pane", "-Z", "-t", objectivesPane).CombinedOutput()
			if zoomErr != nil {
				t.Fatalf("private sidebar rezoom: %v %s", zoomErr, out)
			}
		}
		text, e = script("text")
		if e == nil && strings.Contains(text, "Runtime result") && strings.Contains(text, "Commander to review the finished result") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if e != nil || !strings.Contains(text, "Runtime result") {
		t.Fatalf("canonical workstream did not render: %v\n%s", e, text)
	}
	if !strings.Contains(text, "Commander to review the finished result") || strings.Contains(text, "Manager review overdue") {
		t.Fatalf("canonical pending result did not render honestly:\n%s", text)
	}
	if strings.Contains(text, "real claude is disabled") || strings.Contains(text, "real codex is disabled") {
		t.Fatalf("cockpit attempted a real subscription runtime:\n%s", text)
	}
	prompt, e := os.ReadFile(filepath.Join(iso, "home", ".config", "opencode", "agents", "commander.md"))
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(prompt), "cmdr plan apply") || strings.Contains(string(prompt), "bias toward delegation") {
		t.Fatal("active cockpit installed the legacy delegation prompt")
	}
	command := exec.Command(r.tmux, "-S", filepath.Join(socket, "cockpit.sock"), "list-windows", "-t", "commander", "-F", "#{window_name}")
	windows, e := command.Output()
	if e != nil || strings.TrimSpace(string(windows)) != "commander" {
		t.Fatalf("active startup created legacy Manager windows: %q %v", windows, e)
	}
	t.Logf("Verified active PostgreSQL cockpit via scripts/cockpit-test.sh text:\n%s", text)
}
