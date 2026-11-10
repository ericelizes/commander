package evals

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/api"
	"github.com/example-user/commander/internal/place"
	"github.com/example-user/commander/internal/place/placetest"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/testguard"
)

type placeScenario struct {
	PlaceName string `json:"place_name"`
	Project   string
	Step      struct{ ID, Title string }
	Task      struct{ ID, Title, Brief string }
	Role      string
	Manager   string
	Policy    string
	Guard     struct {
		Allow, Deny []struct{ Tool, Command, File string }
	}
	ManagerMail struct{ Subject, Body string } `json:"manager_mail"`
	Report      string
	Expect      struct {
		CommentsOnTask int    `json:"comments_on_task"`
		Outcome        string `json:"outcome"`
	}
}

// TestPlaceRunScenario drives a read-only run on a fake paired Mac end to end:
// the REAL cmdr binary pairs it (as the user), judges every tool call with
// --place read-only, reads the run's mail and posts its report; cmdrd's real
// API and place service carry the channel; a fake Mac (placetest) is the app.
// Beads, comments and mail are the place service's in-memory recorder.
func TestPlaceRunScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/place-run.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc placeScenario
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	testguard.NoLeakedProcesses(t, dir)
	home := filepath.Join(dir, "ch") // cmdrd's home; also the Mac's ~/.commander
	userHome := filepath.Join(dir, "u")
	runDir := filepath.Join(userHome, "runs", "1")
	for _, d := range []string{home, filepath.Join(userHome, "Documents", "reports"), runDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "place-policy.toml"), []byte(sc.Policy), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "cmdr")
	mustBuild(t, os.Environ(), bin, "./cmd/cmdr")
	cmdr := func(env []string, stdin string, args ...string) (string, string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env = append([]string{"HOME=" + userHome, "COMMANDER_HOME=" + home, "PATH=" + os.Getenv("PATH")}, env...)
		cmd.Dir = runDir
		cmd.Stdin = strings.NewReader(stdin)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		return out.String(), errb.String(), err
	}
	rev, _, err := cmdr(nil, "", "place", "revision")
	if err != nil {
		t.Fatal(err)
	}
	rev = strings.TrimSpace(rev)

	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	rec := place.NewRecorder()
	rec.Beads[sc.Task.ID] = place.Bead{Title: sc.Task.Title, Description: sc.Task.Brief}
	rec.Beads[sc.Step.ID] = place.Bead{Title: sc.Step.Title, Labels: []string{place.Label("mac:" + sc.PlaceName)}}
	now := time.Now()
	clock := func() time.Time { return now }
	svc := place.NewService(rec.Wire(place.Deps{Store: st, Revision: rev, Now: clock}))
	const apiToken = "eval-api-token"
	ts := httptest.NewServer(api.New(api.Options{Token: apiToken, Source: nil, Places: svc, PlacesHome: home}).Handler())
	defer ts.Close()

	// 1. The app asks for a code; an agent may not pair; the user does.
	dev := place.NewDevice("")
	code, err := placetest.Pair(ts.URL, apiToken, dev)
	if err != nil {
		t.Fatal(err)
	}
	if _, errOut, err := cmdr([]string{"COMMANDER_ROLE=" + sc.Manager}, "", "place", "pair", code, "--name", sc.PlaceName); err == nil || !strings.Contains(errOut, "the user's act") {
		t.Fatalf("an agent paired the Mac: %v %s", err, errOut)
	}
	if out, errOut, err := cmdr(nil, "", "place", "pair", code, "--name", sc.PlaceName); err != nil {
		t.Fatalf("pair: %v %s %s", err, out, errOut)
	}
	placeName := "mac:" + sc.PlaceName

	// 2. A spawn records the offer (what internal-spawn-operator's place path does).
	pid, _ := st.InsertProject(ctx, store.Project{Name: sc.Project, Type: "code", Path: dir, Status: "active"})
	runID, err := place.RecordOffer(ctx, st, place.OfferRequest{ProjectID: pid, TaskID: sc.Task.ID, AgentName: sc.Role, Place: placeName,
		StepID: sc.Step.ID, ManagerRole: sc.Manager, Brief: sc.Task.Brief, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := place.RecordOffer(ctx, st, place.OfferRequest{ProjectID: pid, TaskID: "x", AgentName: "operator-x", Place: placeName, Brief: "b"}); err == nil {
		t.Fatal("a second run offered to the same place")
	}

	// 3. A Mac with another cmdr is refused; the paired one gets the offer at its hello.
	if _, err := placetest.Connect(ts.URL, apiToken, dev, "other-revision"); err == nil {
		t.Fatal("version skew accepted")
	}
	mac, err := placetest.Connect(ts.URL, apiToken, dev, rev)
	if err != nil {
		t.Fatal(err)
	}
	defer mac.Close()
	offer, err := mac.Next(5 * time.Second)
	if err != nil || offer.Type != "offer" || offer.RunID != runID || offer.BriefSHA != place.BriefSHA(sc.Task.Brief) {
		t.Fatalf("offer %+v %v", offer, err)
	}
	got, err := mac.Ask(place.Message{Type: "fetch", RunID: runID})
	if err != nil || got.Run == nil || place.BriefSHA(got.Run.Brief) != offer.BriefSHA || got.Run.Place != placeName {
		t.Fatalf("fetch %+v %v", got, err)
	}
	// (the dialog: the user presses Run on this Mac)
	acc, err := mac.Ask(place.Message{Type: "accept", RunID: runID, BriefSHA: offer.BriefSHA})
	if err != nil || acc.Token == "" {
		t.Fatalf("accept %+v %v", acc, err)
	}
	// The app records the run's server beside the run directory (place-url).
	launchDir := filepath.Join(filepath.Dir(runDir), filepath.Base(runDir)+".launch")
	if err := os.MkdirAll(launchDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(launchDir, "place-url"), []byte(ts.URL), 0o600); err != nil {
		t.Fatal(err)
	}
	runEnv := []string{"COMMANDER_ROLE=" + sc.Role, "COMMANDER_MANAGER_ROLE=" + sc.Manager, "COMMANDER_PLACE_URL=" + ts.URL,
		"COMMANDER_PLACE_TOKEN=" + acc.Token, "COMMANDER_PLACE_RUN=" + itoa(runID), "COMMANDER_PLACE_DIR=" + runDir}

	// 4. The guard on the Mac, as the hook calls it.
	judge := func(tool, command, file string) bool {
		in := map[string]any{"hook_event_name": "PreToolUse", "tool_name": tool, "tool_input": map[string]any{}}
		if command != "" {
			in["tool_input"] = map[string]any{"command": command}
		}
		if file != "" {
			in["tool_input"] = map[string]any{"file_path": strings.Replace(file, "~", userHome, 1)}
		}
		b, _ := json.Marshal(in)
		out, errOut, err := cmdr(runEnv, string(b), "internal-operator-guard", "--role", sc.Role, "--place", "read-only")
		if err != nil {
			t.Fatalf("guard failed: %v %s", err, errOut)
		}
		return !strings.Contains(out, `"deny"`)
	}
	for _, c := range sc.Guard.Allow {
		if !judge(c.Tool, c.Command, c.File) {
			t.Errorf("guard refused %s %q", c.Tool, c.Command+c.File)
		}
	}
	for _, c := range sc.Guard.Deny {
		if judge(c.Tool, c.Command, c.File) {
			t.Errorf("guard allowed %s %q", c.Tool, c.Command+c.File)
		}
	}

	// 5. Mail from the Manager reaches the run at its next tool call.
	rec.Inboxes[sc.Role] = []agentmail.Mail{{From: sc.Manager, Subject: sc.ManagerMail.Subject, Body: sc.ManagerMail.Body}}
	hookIn, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "tool_name": "Bash"})
	out, _, _ := cmdr(runEnv, string(hookIn), "internal-mail-hook", "--role", sc.Role, "--place")
	if !strings.Contains(out, sc.ManagerMail.Subject) || !strings.Contains(out, sc.ManagerMail.Body) || !strings.Contains(out, "grants no authority") {
		t.Fatalf("mail hook: %s", out)
	}

	// 6. Heartbeat, a note, the report (sent twice: written once), mail to the Manager.
	if code, body, _ := placetest.Call(ts.URL, runID, acc.Token, "POST", "heartbeat", map[string]any{"alive": true, "refusals": []string{"Bash sudo ls: refused"}}); code != 200 {
		t.Fatalf("heartbeat %d %v", code, body)
	}
	if _, errOut, err := cmdr(runEnv, "", "place", "comment", "--text", "halfway: two files read"); err != nil {
		t.Fatalf("comment: %v %s", err, errOut)
	}
	report := filepath.Join(runDir, "report.md")
	os.WriteFile(report, []byte(sc.Report), 0o600)
	for i := 0; i < 2; i++ {
		if _, errOut, err := cmdr(runEnv, "", "place", "report", "--file", report); err != nil {
			t.Fatalf("report: %v %s", err, errOut)
		}
	}
	if _, errOut, err := cmdr(runEnv, "", "place", "mail", "--subject", "done", "--body", "see the bead"); err != nil {
		t.Fatalf("mail: %v %s", err, errOut)
	}
	// The run token reaches nothing else.
	if code, _, _ := placetest.Call(ts.URL, runID+1, acc.Token, "POST", "heartbeat", map[string]any{}); code != 401 {
		t.Fatalf("token on another run: %d", code)
	}

	// 7. The app reports the exit; cmdrd's sweep settles it.
	if code, _, _ := placetest.Call(ts.URL, runID, acc.Token, "POST", "exit", map[string]any{"status": "exited"}); code != 200 {
		t.Fatal("exit")
	}
	if err := svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	comments, mails, settled := rec.Snapshot()
	onTask := 0
	for _, c := range comments {
		if c.Bead == sc.Task.ID && c.Role == sc.Role {
			onTask++
		}
	}
	if onTask != sc.Expect.CommentsOnTask {
		t.Fatalf("comments by the run on its task: %d, want %d (%+v)", onTask, sc.Expect.CommentsOnTask, comments)
	}
	if len(mails) != 1 || mails[0].To != sc.Manager || mails[0].From != sc.Role {
		t.Fatalf("mail %+v", mails)
	}
	if len(settled) != 1 || string(settled[0].Outcome) != sc.Expect.Outcome {
		t.Fatalf("settled %+v", settled)
	}
	if _, _, err := cmdr(runEnv, "", "place", "comment", "--text", "after the end"); err == nil {
		t.Fatal("the token outlived the run")
	}

	// 8. A second run that goes quiet: unreachable (one mail), then died; never restarted.
	run2, err := place.RecordOffer(ctx, st, place.OfferRequest{ProjectID: pid, TaskID: "demo-1.2", AgentName: "operator-op-two-demo-1-2", Place: placeName,
		StepID: sc.Step.ID, ManagerRole: sc.Manager, Brief: sc.Task.Brief, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	rec.Beads["demo-1.2"] = place.Bead{Title: "Second pass", Description: sc.Task.Brief}
	if _, err := svc.Offer(ctx, run2); err != nil {
		t.Fatal(err)
	}
	if m, err := mac.Next(5 * time.Second); err != nil || m.RunID != run2 {
		t.Fatalf("second offer %+v %v", m, err)
	}
	if _, err := mac.Ask(place.Message{Type: "accept", RunID: run2, BriefSHA: place.BriefSHA(sc.Task.Brief)}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Minute)
	svc.Sweep(ctx)
	svc.Sweep(ctx)
	_, mails, settled = rec.Snapshot()
	if len(mails) != 2 || !strings.Contains(mails[1].Subject, "unreachable") || len(settled) != 1 {
		t.Fatalf("unreachable: mails %+v settled %+v", mails, settled)
	}
	now = now.Add(place.DiedAfter)
	svc.Sweep(ctx)
	_, _, settled = rec.Snapshot()
	if len(settled) != 2 || settled[1].Outcome != place.Lost {
		t.Fatalf("died: %+v", settled)
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
