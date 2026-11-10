package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
)

// TestCapacityEngineSnapshotRecovery exercises the real start planner and Source,
// with only the provider and bead backend replaced. Optional live mode renders
// the HTTP snapshot in an already isolated cockpit, never the user's server.
func TestCapacityEngineSnapshotRecovery(t *testing.T) {
	ctx := context.Background()
	h := accountHarness(t, "spare")
	role := "manager-sample-root"
	h.role(role)
	if err := WriteRuntime(h.home, role, RuntimeClaude); err != nil {
		t.Fatal(err)
	}
	st := costStore(t) // finish current-schema initialization before readers start
	pid, err := st.InsertProject(ctx, store.Project{Name: "sample", Type: "code", Path: h.work, ActivatedAt: 1, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertWorkstreamManager(ctx, store.WorkstreamManager{ProjectID: pid, WorkstreamBeadID: "sample-root", SessionFile: filepath.Join(h.home, "session")}); err != nil {
		t.Fatal(err)
	}
	bc := &beads.MockClient{ListResult: []beads.Bead{
		{ID: "sample-root", Title: "Capacity example", Status: "open", Labels: []string{"kind:objective"}},
		{ID: "sample-root.1", Title: "First track", Status: "open", Labels: []string{"kind:objective", "kind:workstream"}},
		{ID: "sample-root.2", Title: "Second track", Status: "open", Labels: []string{"kind:objective", "kind:workstream"}},
	}, ParentsResult: map[string]string{"sample-root": "", "sample-root.1": "sample-root", "sample-root.2": "sample-root"}}
	if err := st.SetAgentState(ctx, role, store.AgentIdle, ""); err != nil {
		t.Fatal(err)
	}
	src := &livedata.Source{Deps: livedata.Deps{Store: st, Beads: bc}, ManagerCapacity: h.e.CapacityFailures,
		Unread: func(context.Context, string) (int, error) { return 1, nil },
		Chats: func() map[string]livedata.ChatState {
			out := map[string]livedata.ChatState{}
			for r, c := range h.e.Live() {
				out[r] = livedata.ChatState{State: c.State, Detail: c.Detail}
			}
			return out
		},
	}
	capture := capacityCockpit(t, src)
	now := time.Now()
	h.start(role)
	h.e.Usage().MarkOut(RuntimeClaude, "spare", role, now.Add(time.Hour).Unix(), now)
	limited := h.subscribe(role)
	if err := h.e.Send(ctx, role, "usagelimit"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, limited, func(ev Event) bool { return ev.Kind == KindNotice && strings.Contains(ev.Text, "Not moved:") })
	t.Log("fake provider emitted rejected rate_limit_event and error result; Engine refused automatic move")
	limited.Close()
	if err := h.e.Stop(ctx, role); err != nil {
		t.Fatal(err)
	}
	if _, err := h.e.Start(ctx, role, StartOptions{}); !errors.Is(err, ErrNoUsage) {
		t.Fatalf("exhausted start: %v", err)
	}
	if h.e.Running(role) {
		t.Fatal("exhausted provider started")
	}
	snap := src.Snapshot(ctx)
	if snap.Degraded != nil || len(snap.Workstreams) != 1 {
		t.Fatalf("snapshot: %+v", snap)
	}
	row := snap.Display.Workstreams[0]
	if row.Text != "Manager out of usage" || len(row.Tracks) != 2 {
		t.Fatalf("refusal row: %+v", row)
	}
	for _, tr := range row.Tracks {
		if tr.Text != "Manager out of usage" {
			t.Fatalf("refusal track: %+v", tr)
		}
	}
	capture("refused", "Manager out of usage")
	t.Logf("refusal: Start=%v running=%v capacity=%v; parent and two tracks out of usage", ErrNoUsage, h.e.Running(role), h.e.CapacityFailures())
	// Restore only the alternate account. Start must really launch a fake process
	// there and complete a turn, retaining the user's Claude pin.
	h.e.Usage().Record(RuntimeClaude, "spare", role, []byte(`{"status":"allowed"}`), time.Now())
	status := h.start(role)
	if status.Account != "spare" || status.Runtime != RuntimeClaude || ManagerPin(h.home, role) != RuntimeClaude {
		t.Fatalf("recovery: %+v", status)
	}
	sub := h.subscribe(role)
	if err := h.e.Send(ctx, role, "capacity recovered"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, sub, isKind(KindResult))
	snap = src.Snapshot(ctx)
	if len(snap.ManagerCapacity) != 0 || snap.Display.Workstreams[0].Text == "Manager out of usage" {
		t.Fatalf("stale refusal: %+v", snap.Display)
	}
	capture("recovered", "Not started: Manager to plan")
	if artifacts := os.Getenv("CMDR_CAPACITY_COCKPIT_ARTIFACTS"); artifacts != "" {
		raw, err := os.ReadFile(filepath.Join(h.home, "chat", role+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(artifacts, "provider.jsonl"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("recovery: running=%v runtime=%s account=%s pin=%s capacity=%v; fake provider turn_result received", h.e.Running(role), status.Runtime, status.Account, ManagerPin(h.home, role), h.e.CapacityFailures())
}

// Live mode is deliberately specific to this flow. The caller owns an isolated
// cockpit-test.sh server and supplies its home; no production daemon is used.
func capacityCockpit(t *testing.T, src *livedata.Source) func(string, string) {
	t.Helper()
	iso := os.Getenv("CMDR_CAPACITY_COCKPIT_HOME")
	if iso == "" {
		return func(string, string) {}
	}
	canonical, err := filepath.EvalSymlinks(iso)
	if err != nil {
		t.Fatal(err)
	}
	iso = canonical
	sockdir := os.Getenv("CMDR_CAPACITY_COCKPIT_SOCKET_DIR")
	binary := os.Getenv("CMDR_CAPACITY_COCKPIT_BINARY")
	artifacts := os.Getenv("CMDR_CAPACITY_COCKPIT_ARTIFACTS")
	if sockdir == "" || binary == "" || artifacts == "" {
		t.Fatal("live capacity mode requires socket directory, binary and artifacts")
	}
	owner, err := os.ReadFile(filepath.Join(sockdir, ".cockpit-owner"))
	if err != nil || strings.TrimSpace(string(owner)) != iso {
		t.Fatal("not this isolated cockpit's socket")
	}
	if _, err := os.Stat(filepath.Join(iso, ".cockpit-isolated")); err != nil {
		t.Fatal(err)
	}
	tm := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("tmux", append([]string{"-S", filepath.Join(sockdir, "cockpit.sock")}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("private tmux %v: %v %s", args, err, out)
		}
		return string(out)
	}
	token, err := os.ReadFile(filepath.Join(iso, ".server-token"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(tm("show-environment", "-g", "COCKPIT_SERVER_TOKEN")) != "COCKPIT_SERVER_TOKEN="+strings.TrimSpace(string(token)) {
		t.Fatal("foreign server")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/snapshot" || r.Header.Get("Authorization") != "Bearer capacity-test" {
			http.Error(w, "refused", 403)
			return
		}
		json.NewEncoder(w).Encode(src.Snapshot(r.Context()))
	}))
	t.Cleanup(server.Close)
	home := filepath.Join(iso, "capacity-reader")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"api-url": server.URL, "api-token": "capacity-test"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(artifacts, 0700); err != nil {
		t.Fatal(err)
	}
	// A private window keeps the standard cockpit's fake provider pane intact.
	tm("new-window", "-d", "-t", "commander:", "-n", "capacity-proof", "/bin/sh")
	tm("resize-window", "-t", "commander:capacity-proof", "-x", "220", "-y", "50")
	tm("select-window", "-t", "commander:capacity-proof")
	t.Cleanup(func() { tm("kill-window", "-t", "commander:capacity-proof") })
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	return func(stage, want string) {
		t.Helper()
		// Fetch a fresh HTTP snapshot with the actual cmdr board renderer; there is
		// no hand-authored snapshot file or substituted display block.
		command := "env -i PATH=/usr/bin:/bin HOME=" + quote(home) + " COMMANDER_HOME=" + quote(home) + " " + quote(binary) + " board; exec /bin/sh"
		tm("respawn-pane", "-k", "-t", "commander:capacity-proof.0", command)
		var text string
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			text = tm("capture-pane", "-p", "-t", "commander:capacity-proof.0")
			if strings.Count(text, want) >= 3 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if strings.Count(text, want) < 3 {
			t.Fatalf("%s cockpit missing parent/two tracks %q:\n%s", stage, want, text)
		}
		capture := exec.Command("../../scripts/cockpit-test.sh", "text")
		capture.Env = append(os.Environ(), "COCKPIT_SESSION=commander:capacity-proof", "COCKPIT_HOME="+iso, "COCKPIT_TMUX_TMPDIR="+sockdir, "COCKPIT_CMDR="+binary, "COCKPIT_FAKE_OPENCODE="+filepath.Join(filepath.Dir(binary), "opencode"))
		all, err := capture.CombinedOutput()
		if err != nil {
			t.Fatalf("cockpit-test.sh text: %v %s", err, all)
		}
		text = string(all)
		if strings.Count(text, want) < 3 {
			t.Fatalf("authoritative capture missing %q: %s", want, text)
		}
		if err := os.WriteFile(filepath.Join(artifacts, stage+".txt"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		snapshot := src.Snapshot(context.Background())
		raw, err := json.MarshalIndent(snapshot, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(artifacts, stage+".json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s private cockpit:\n%s", stage, text)
	}
}
