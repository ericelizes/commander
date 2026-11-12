package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/api"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/execution"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/place"
	"github.com/example-user/commander/internal/place/placetest"
	"github.com/example-user/commander/internal/store"
)

// Real PG scheduler, paired fake device, guarded CLI and attributed bead result.
// No real Mac or model process participates.
func TestPostgresMacExecutionScenario(t *testing.T) {
	st, dsn := pgtest.Open(t)
	var sc struct {
		Place, Bead, Brief, Report string
		Deny                       []string
	}
	raw, err := os.ReadFile("scenarios/mac-execution.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	home, dir := t.TempDir(), t.TempDir()
	if err = os.MkdirAll(filepath.Join(dir, ".beads"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMMANDER_HOME", home)
	if err = os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); err != nil {
		t.Fatal(err)
	}
	bc := beads.New()
	pid, err := st.InsertProject(ctx, store.Project{Name: "mac-eval", Path: dir, Type: "code", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DB().Exec(`INSERT INTO bead_tasks(project_id,id,parent_id,title,description,acceptance_criteria,labels,created_at,updated_at) VALUES(?,'mac-root',NULL,'Mac execution result','','','["kind:objective","kind:workstream"]','now','now'),(?,?,'mac-root','Mac task',?,'Result recorded','["kind:objective"]','now','now'),(?,'foreign',NULL,'Foreign task','Foreign work','Untouched','[]','now','now')`, pid, pid, sc.Bead, sc.Brief, pid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.SetExecutionReady(ctx, pid, sc.Bead, "commander", 1, 1, true, []string{"mac"}); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SetOrchestrationControl(ctx, 1, "active", "isolated Mac eval"); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "cmdr")
	mustBuild(t, os.Environ(), bin, "./cmd/cmdr")
	cmd := func(env []string, input string, args ...string) (string, error) {
		c := exec.Command(bin, args...)
		c.Dir = dir
		c.Env = append([]string{"HOME=" + home, "COMMANDER_HOME=" + home, "PATH=" + os.Getenv("PATH")}, env...)
		c.Stdin = strings.NewReader(input)
		out, e := c.CombinedOutput()
		return string(out), e
	}
	// A per-step runtime choice must coexist with Mac capability routing.
	if out, e := cmd(nil, "", "internal-set-objective", "--project", "mac-eval", "--bead", sc.Bead, "--runtime", "claude"); e != nil {
		t.Fatal(e, out)
	}
	if b, e := bc.Show(ctx, dir, sc.Bead); e != nil || b == nil || !strings.Contains(strings.Join(b.Labels, ","), "runtime:claude") {
		t.Fatal("runtime preference was not saved", b, e)
	}
	rev, err := cmd(nil, "", "place", "revision")
	if err != nil {
		t.Fatal(err, rev)
	}
	deps := place.NewRecorder().Wire(place.Deps{Store: st, Revision: strings.TrimSpace(rev)})
	deps.ReadBead = func(ctx context.Context, path, id string) (place.Bead, error) {
		b, e := beads.FreshShow(ctx, bc, path, id)
		if e != nil {
			return place.Bead{}, e
		}
		return place.Bead{Title: b.Title, Description: b.Description, Status: b.Status, Labels: b.Labels}, nil
	}
	var server *api.Server
	deps.RunComment = func(ctx context.Context, r *place.Run, text string) error { return server.DoorRunComment(ctx, r, text) }
	svc := place.NewService(deps)
	server = api.New(api.Options{Token: "synthetic-token", StateStore: st, Beads: bc, Places: svc, PlacesHome: home})
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	dev := place.NewDevice("")
	code, err := placetest.Pair(ts.URL, "synthetic-token", dev)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"api-url": ts.URL, "api-token": "synthetic-token"} {
		if err = os.WriteFile(filepath.Join(home, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := cmd(nil, "", "place", "pair", code, "--name", strings.TrimPrefix(sc.Place, "mac:"))
	if err != nil {
		t.Fatal(err, out)
	}
	mac, err := placetest.Connect(ts.URL, "synthetic-token", dev, strings.TrimSpace(rev))
	if err != nil {
		t.Fatal(err)
	}
	defer mac.Close()
	for _, slot := range []struct {
		name string
		caps []string
	}{{"vm-code", []string{"code"}}, {"place:" + sc.Place, []string{"code", "mac"}}} {
		if err = st.ConfigureExecutionSlot(ctx, slot.name, slot.caps, true); err != nil {
			t.Fatal(err)
		}
	}
	codeWorker := execution.Worker{Store: st, Capacity: 2}
	if err = codeWorker.PassSlot(ctx, "vm-code"); err != nil {
		t.Fatal("code-only slot tried to launch Mac work", err)
	}
	var n int
	if err = st.DB().QueryRow(`SELECT count(*) FROM agent_runs`).Scan(&n); err != nil || n != 0 {
		t.Fatal("code slot claimed Mac bead", n, err)
	}
	worker := execution.Worker{Store: st, Place: sc.Place, Capacity: 2, OfferPlace: svc.Offer}
	if err = worker.PassSlot(ctx, "place:"+sc.Place); err != nil {
		t.Fatal(err)
	}
	offer, err := mac.Next(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := mac.Ask(place.Message{Type: "fetch", RunID: offer.RunID})
	if err != nil {
		t.Fatal(err)
	}
	if fetched.Run.TaskID != sc.Bead || fetched.Run.Brief != sc.Brief {
		t.Fatalf("wrong assignment %+v", fetched.Run)
	}
	accepted, err := mac.Ask(place.Message{Type: "accept", RunID: offer.RunID, BriefSHA: offer.BriefSHA})
	if err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(home, "runs", fmt.Sprint(offer.RunID))
	if err = os.MkdirAll(runDir, 0700); err != nil {
		t.Fatal(err)
	}
	launch := runDir + ".launch"
	if err = os.MkdirAll(launch, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(launch, "place-url"), []byte(ts.URL), 0600); err != nil {
		t.Fatal(err)
	}
	run, err := st.GetAgentRun(ctx, offer.RunID)
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"COMMANDER_ROLE=" + run.AgentName, "COMMANDER_PLACE_URL=" + ts.URL, "COMMANDER_PLACE_TOKEN=" + accepted.Token, fmt.Sprintf("COMMANDER_PLACE_RUN=%d", offer.RunID), "COMMANDER_PLACE_DIR=" + runDir}
	for _, command := range sc.Deny {
		input, _ := json.Marshal(map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": command}})
		out, err = cmd(env, string(input), "internal-operator-guard", "--role", run.AgentName, "--place", "read-only")
		if err != nil || !strings.Contains(out, `"deny"`) {
			t.Fatalf("guard accepted %q: %v %s", command, err, out)
		}
	}
	// The simulated Operator runs a harmless local command after the real hook.
	if err = os.WriteFile(filepath.Join(runDir, "input.txt"), []byte(sc.Report), 0600); err != nil {
		t.Fatal(err)
	}
	guard, _ := json.Marshal(map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "cat input.txt > report.md"}})
	out, err = cmd(env, string(guard), "internal-operator-guard", "--role", run.AgentName, "--place", "read-only")
	if err != nil || strings.Contains(out, `"deny"`) {
		t.Fatal(err, out)
	}
	fake := exec.Command("sh", "-c", "cat input.txt > report.md")
	fake.Dir = runDir
	if output, e := fake.CombinedOutput(); e != nil {
		t.Fatal(e, string(output))
	}
	for i := 0; i < 2; i++ {
		out, err = cmd(env, "", "place", "report", "--file", filepath.Join(runDir, "report.md"))
		if err != nil {
			t.Fatal(err, out)
		}
	}
	foreignEnv := append(append([]string{}, env...), fmt.Sprintf("COMMANDER_RUN_ID=%d", run.ID), "COMMANDER_RUN_SESSION="+run.OpencodeSessionID)
	if err = bc.AddComment(beads.WithDoorEnv(ctx, foreignEnv), dir, "foreign", "forbidden"); err == nil {
		t.Fatal("context-attributed foreign write accepted through HTTP door")
	}
	if out, err = cmd(foreignEnv, "", "internal-comment", "--project", "mac-eval", "--bead", "foreign", "--text", "forbidden"); err == nil {
		t.Fatal("foreign write accepted")
	}
	if code, _, _ := placetest.Call(ts.URL, run.ID+1, accepted.Token, "POST", "comment", map[string]any{"text": "forbidden", "client_id": "foreign"}); code != 401 {
		t.Fatal("run token reached foreign run", code)
	}
	if code, body, e := placetest.Call(ts.URL, run.ID, accepted.Token, "POST", "exit", map[string]any{"status": "exited"}); e != nil || code != 200 {
		t.Fatal(code, body, e)
	}
	var result int64
	var summary, status string
	if err = st.DB().QueryRow(`SELECT result_run_id,result_summary,status FROM bead_tasks WHERE project_id=? AND id=?`, pid, sc.Bead).Scan(&result, &summary, &status); err != nil {
		t.Fatal(err)
	}
	if result != run.ID || summary != sc.Report || status == "closed" {
		t.Fatalf("missing unaccepted result: %d %q %s", result, summary, status)
	}
	if err = st.DB().QueryRow(`SELECT count(*) FROM bead_comments WHERE project_id=? AND bead_id=? AND run_id=?`, pid, sc.Bead, run.ID).Scan(&n); err != nil || n != 1 {
		t.Fatal("report dedup/attribution", n, err)
	}
	var foreign int
	if err = st.DB().QueryRow(`SELECT count(*) FROM bead_comments WHERE project_id=? AND bead_id='foreign'`, pid).Scan(&foreign); err != nil || foreign != 0 {
		t.Fatal("foreign bead modified", foreign, err)
	}
	if code, _, _ := placetest.Call(ts.URL, run.ID, accepted.Token, "POST", "heartbeat", map[string]any{}); code != 401 {
		t.Fatal("exit retained token", code)
	}
	t.Logf("Mac slot claimed %s, guarded simulated Operator ran, PostgreSQL result %d recorded once; code-only slot and foreign writes refused", sc.Bead, result)
	verifyMacResultCockpit(t, bin, dsn)
}
