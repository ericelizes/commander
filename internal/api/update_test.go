package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/example-user/commander/internal/binstage"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/updatecheck"
)

const mainRev = "1111111111111111111111111111111111111111"

type fakeSteps struct {
	mu        sync.Mutex
	calls     []string
	stageErr  error
	busy      string
	branch    string
	dirty     bool
	upToDate  bool
	main      string // what Check reports as main's tip
	noGUI     error
	answer    bool // what the person presses
	promptErr error
	prompts   int
	text      string
	release   chan struct{} // when set, Prompt waits for it
	restart   chan struct{}

	restartDone chan error // what Restart hands back (nil = no result)
	exported    string
	buildSrc    string
	binModified bool
	binErr      error
	binRevOver  string            // when set, the revision the built binaries carry
	prevRev     map[string]string // base name (cmdr.prev) -> revision the installed .prev binary carries
}

func (f *fakeSteps) binRev() string {
	if f.binRevOver != "" {
		return f.binRevOver
	}
	return f.main
}

func (f *fakeSteps) record(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakeSteps) got() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, ",")
}

func (f *fakeSteps) promptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prompts
}

// newTestUpdater builds an Updater whose steps are all fakes over temp dirs;
// the person confirms by default.
func newTestUpdater(t *testing.T) (*Updater, *fakeSteps) {
	t.Helper()
	home, repo, inst := t.TempDir(), t.TempDir(), t.TempDir()
	f := &fakeSteps{branch: "main", main: mainRev, answer: true, restart: make(chan struct{}, 1)}
	os.WriteFile(filepath.Join(inst, "VERSION"), []byte("0000000000000000000000000000000000000000\n"), 0o644)
	os.WriteFile(filepath.Join(inst, "cmdr.prev"), []byte("x"), 0o755)
	u := &Updater{
		Home: home, Repo: repo, InstallDir: inst, LocalBin: t.TempDir(),
		Check: func(context.Context) updatecheck.Status {
			f.mu.Lock()
			defer f.mu.Unlock()
			return updatecheck.Status{Main: f.main, Behind: 2, Dirty: f.dirty, UpToDate: f.upToDate,
				Commits: []updatecheck.Commit{{Hash: f.main, Subject: "feat: x\nforged line", Review: updatecheck.NotConfirmed}}}
		},
		PromptAvailable: func() error { return f.noGUI },
		Prompt: func(_ context.Context, _, text string) (bool, error) {
			f.mu.Lock()
			f.prompts++
			f.text = text
			rel := f.release
			f.mu.Unlock()
			if rel != nil {
				<-rel
			}
			return f.answer, f.promptErr
		},
		Export: func(_ context.Context, _, target, dir string) (func(), error) {
			f.mu.Lock()
			f.exported = target
			f.mu.Unlock()
			os.MkdirAll(dir, 0o755)
			return func() { os.RemoveAll(dir) }, nil
		},
		Build: func(_ context.Context, src, stageDir string, out io.Writer) error {
			f.record("stage")
			f.mu.Lock()
			f.buildSrc = src
			f.mu.Unlock()
			if f.stageErr != nil {
				return f.stageErr
			}
			os.MkdirAll(stageDir, 0o755)
			for _, n := range []string{"cmdr", "cmdrd"} {
				os.WriteFile(filepath.Join(stageDir, n), []byte("bin "+n), 0o755)
			}
			os.WriteFile(filepath.Join(stageDir, "VERSION"), []byte(f.main+"\n"), 0o644)
			io.WriteString(out, "built\n")
			return nil
		},
		BinaryRevision: func(path string) (string, bool, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if rev, ok := f.prevRev[filepath.Base(path)]; ok {
				return rev, false, nil
			}
			return f.binRev(), f.binModified, f.binErr
		},
		Install: func(o binstage.Options) error {
			f.record("install")
			o.Repo = "" // the temp repo is not a go checkout
			return binstage.Install(o)
		},
		Rollback: func(o binstage.Options) error { f.record("rollback"); return binstage.Rollback(o) },
		Restart: func(io.Writer) (<-chan error, error) {
			f.record("restart")
			f.restart <- struct{}{}
			return f.restartDone, nil
		},
		RestartWatch: 200 * time.Millisecond,
		Busy:         func() string { return f.busy },
		Branch:       func(context.Context, string) (string, error) { return f.branch, nil },
	}
	return u, f
}

func updateServer(t *testing.T, u *Updater) *httptest.Server {
	t.Helper()
	srv := New(Options{Token: "tok", Source: nopSource, Update: u})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func call(t *testing.T, ts *httptest.Server, method, path string, body any, extra map[string]string) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, ts.URL+path, rd)
	req.Header.Set("Authorization", "Bearer tok")
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func install(t *testing.T, ts *httptest.Server) (int, map[string]any) {
	return call(t, ts, "POST", "/api/update/install", map[string]string{"action": "install"}, nil)
}

func waitJob(t *testing.T, ts *httptest.Server, state string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, st := call(t, ts, "GET", "/api/update", nil, nil)
		if j, ok := st["job"].(map[string]any); ok && j["state"] == state {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job never reached %s", state)
	return nil
}

func auditResults(t *testing.T, u *Updater) []string {
	raw, err := os.ReadFile(filepath.Join(u.Home, "logs", "update-install.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var m map[string]string
		json.Unmarshal([]byte(l), &m)
		out = append(out, m["result"])
	}
	return out
}

func TestUpdateStatus_CachedForAMinute(t *testing.T) {
	u, _ := newTestUpdater(t)
	calls := 0
	check := u.Check
	u.Check = func(ctx context.Context) updatecheck.Status { calls++; return check(ctx) }
	now := time.Unix(1_800_000_000, 0)
	u.Now = func() time.Time { return now }
	ts := updateServer(t, u)
	for i := 0; i < 3; i++ {
		call(t, ts, "GET", "/api/update", nil, nil)
	}
	if calls != 1 {
		t.Fatalf("check ran %d times inside the cache window", calls)
	}
	now = now.Add(61 * time.Second)
	_, st := call(t, ts, "GET", "/api/update", nil, nil)
	if calls != 2 || st["behind"] != float64(2) {
		t.Fatalf("calls %d, status %v", calls, st)
	}
	if cmds, _ := st["commands"].([]any); len(cmds) != 3 {
		t.Errorf("commands = %v", st["commands"])
	}
}

func TestInstall_NeverBuildsWithoutConfirmation(t *testing.T) {
	cases := map[string]func(*fakeSteps){
		"cancel":  func(f *fakeSteps) { f.answer = false },
		"timeout": func(f *fakeSteps) { f.answer, f.promptErr = false, context.DeadlineExceeded },
	}
	for name, set := range cases {
		u, f := newTestUpdater(t)
		set(f)
		ts := updateServer(t, u)
		if code, out := install(t, ts); code != http.StatusAccepted {
			t.Fatalf("%s: %d %v", name, code, out)
		}
		waitJob(t, ts, JobDeclined)
		if f.got() != "" || binstage.Revision(u.InstallDir) == mainRev {
			t.Errorf("%s: steps ran: %s", name, f.got())
		}
		if res := auditResults(t, u); strings.Join(res, ",") != "asked,declined" {
			t.Errorf("%s: audit = %v", name, res)
		}
	}
}

func TestInstall_NoDialogToolRefusesWithoutPrompting(t *testing.T) {
	u, f := newTestUpdater(t)
	f.noGUI = errors.New("no display")
	ts := updateServer(t, u)
	code, out := install(t, ts)
	if code != http.StatusConflict || !strings.Contains(out["error"].(string), "terminal commands") {
		t.Errorf("%d %v", code, out)
	}
	if f.promptCount() != 0 || f.got() != "" {
		t.Errorf("prompted %d, steps %s", f.promptCount(), f.got())
	}
}

func TestUpdateStatus_SaysWhereCmdrdRunsAndWhyNoDialog(t *testing.T) {
	u, f := newTestUpdater(t)
	ts := updateServer(t, u)
	_, st := call(t, ts, "GET", "/api/update", nil, nil)
	if st["host"] != hostName() || st["no_dialog"] != nil {
		t.Fatalf("with a dialog: %v", st)
	}
	f.noGUI = errors.New("no display")
	_, st = call(t, ts, "GET", "/api/update", nil, nil)
	if st["no_dialog"] != "no display" || st["repo"] != u.Repo {
		t.Errorf("headless: no_dialog %v, repo %v", st["no_dialog"], st["repo"])
	}
}

func TestInstall_ConfirmBuildsExactlyTheNamedRevision(t *testing.T) {
	u, f := newTestUpdater(t)
	ts := updateServer(t, u)
	code, out := install(t, ts)
	if code != http.StatusAccepted {
		t.Fatalf("install: %d %v", code, out)
	}
	<-f.restart
	j := waitJob(t, ts, JobRestarting)
	if f.got() != "stage,install,restart" {
		t.Errorf("steps = %s", f.got())
	}
	if binstage.Revision(u.InstallDir) != mainRev || j["target"] != mainRev {
		t.Errorf("installed %q, target %v", binstage.Revision(u.InstallDir), j["target"])
	}
	// the dialog names the full short hash, the count and the commit, on one line each
	for _, want := range []string{mainRev[:8], "2 commits", "feat: x forged line", "not confirmed"} {
		if !strings.Contains(f.text, want) {
			t.Errorf("dialog text lacks %q:\n%s", want, f.text)
		}
	}
	if strings.Contains(f.text, "\nforged line") {
		t.Errorf("a newline in a commit subject reached the dialog:\n%s", f.text)
	}
	if res := auditResults(t, u); strings.Join(res, ",") != "asked,confirmed,installed" {
		t.Errorf("audit = %v", res)
	}
}

func TestInstall_MainMovedAbortsAfterConfirmation(t *testing.T) {
	u, f := newTestUpdater(t)
	f.release = make(chan struct{})
	ts := updateServer(t, u)
	install(t, ts)
	f.mu.Lock()
	f.main = "2222222222222222222222222222222222222222" // main moves while the dialog is open
	f.mu.Unlock()
	close(f.release)
	j := waitJob(t, ts, JobFailed)
	if !strings.Contains(j["error"].(string), "main moved, nothing installed, try again") || f.got() != "" {
		t.Errorf("job %v, steps %s", j, f.got())
	}
}

func TestInstall_StagedRevisionMustBeTheConfirmedOne(t *testing.T) {
	u, f := newTestUpdater(t)
	build := u.Build
	u.Build = func(ctx context.Context, src, stageDir string, out io.Writer) error {
		err := build(ctx, src, stageDir, out)
		os.WriteFile(filepath.Join(stageDir, "VERSION"), []byte("3333\n"), 0o644)
		return err
	}
	ts := updateServer(t, u)
	install(t, ts)
	j := waitJob(t, ts, JobFailed)
	if !strings.Contains(j["error"].(string), "nothing installed") || f.got() != "stage" {
		t.Errorf("job %v, steps %s", j, f.got())
	}
}

// BLOCKER1: the build gets the export of the confirmed commit, never the live
// checkout, and binaries that do not carry exactly that revision never install.
func TestInstall_BLOCKER1_BuildsTheExportOfTheConfirmedCommit(t *testing.T) {
	u, f := newTestUpdater(t)
	ts := updateServer(t, u)
	install(t, ts)
	<-f.restart
	waitJob(t, ts, JobRestarting)
	if f.exported != mainRev {
		t.Errorf("exported %q, want the confirmed commit", f.exported)
	}
	if f.buildSrc == u.Repo || !strings.HasPrefix(f.buildSrc, filepath.Join(u.Home, "update-build")) {
		t.Errorf("built in %q, not the export under the update-build dir", f.buildSrc)
	}
	if _, err := os.Stat(f.buildSrc); !os.IsNotExist(err) {
		t.Errorf("export %s left behind", f.buildSrc)
	}
	if _, err := os.Stat(f.buildSrc + ".staged"); !os.IsNotExist(err) {
		t.Error("staged dir left behind")
	}
}

func TestInstall_BLOCKER1_BinariesMustCarryExactlyTheTarget(t *testing.T) {
	cases := map[string]func(*fakeSteps){
		"other revision": func(f *fakeSteps) { f.binRevOver = "2222222222222222222222222222222222222222" },
		"modified":       func(f *fakeSteps) { f.binModified = true },
		"unreadable":     func(f *fakeSteps) { f.binErr = errors.New("not a go binary") },
	}
	for name, set := range cases {
		u, f := newTestUpdater(t)
		set(f)
		ts := updateServer(t, u)
		install(t, ts)
		j := waitJob(t, ts, JobFailed)
		if !strings.Contains(j["error"].(string), "nothing installed") || f.got() != "stage" {
			t.Errorf("%s: job %v, steps %s", name, j, f.got())
		}
		if binstage.Revision(u.InstallDir) == mainRev {
			t.Errorf("%s: installed", name)
		}
		if _, err := os.Stat(f.buildSrc); !os.IsNotExist(err) {
			t.Errorf("%s: export left behind after failure", name)
		}
	}
}

func TestInstall_RateLimitIsGlobalNotPerTarget(t *testing.T) {
	u, f := newTestUpdater(t)
	f.answer = false
	now := time.Unix(1_800_000_000, 0)
	u.Now = func() time.Time { return now }
	ts := updateServer(t, u)
	install(t, ts)
	waitJob(t, ts, JobDeclined)
	// a new main commit and the other action do not earn a fresh prompt
	f.mu.Lock()
	f.main = "2222222222222222222222222222222222222222"
	f.mu.Unlock()
	for _, action := range []string{"install", "rollback"} {
		code, out := call(t, ts, "POST", "/api/update/install", map[string]string{"action": action}, nil)
		if code != http.StatusTooManyRequests || !strings.Contains(out["error"].(string), "a confirmation was just shown") {
			t.Errorf("%s: %d %v", action, code, out)
		}
	}
	if f.promptCount() != 1 {
		t.Errorf("prompted %d times", f.promptCount())
	}
	// three per hour, one per five minutes
	for i := 0; i < 2; i++ {
		now = now.Add(5*time.Minute + time.Second)
		if code, out := install(t, ts); code != http.StatusAccepted {
			t.Fatalf("ask %d: %d %v", i+2, code, out)
		}
		waitJob(t, ts, JobDeclined)
	}
	now = now.Add(5*time.Minute + time.Second)
	if code, out := install(t, ts); code != http.StatusTooManyRequests || !strings.Contains(out["error"].(string), "this hour") {
		t.Errorf("fourth ask within the hour: %d %v", code, out)
	}
	now = now.Add(time.Hour)
	if code, _ := install(t, ts); code != http.StatusAccepted {
		t.Errorf("after an hour: %d", code)
	}
	for i := 0; i < 500 && strings.Count(strings.Join(auditResults(t, u), ","), "declined") < 4; i++ { // let the last job settle before the temp dirs go
		time.Sleep(10 * time.Millisecond)
	}
}

func TestInstall_RateLimitAppliesBeforeCheck(t *testing.T) {
	u, f := newTestUpdater(t)
	f.answer = false
	checks := 0
	check := u.Check
	u.Check = func(ctx context.Context) updatecheck.Status { checks++; return check(ctx) }
	ts := updateServer(t, u)
	install(t, ts)
	waitJob(t, ts, JobDeclined)
	before := checks
	for i := 0; i < 20; i++ {
		if code, _ := install(t, ts); code != http.StatusTooManyRequests {
			t.Fatalf("flood %d answered %d", i, code)
		}
	}
	if checks != before {
		t.Errorf("Check ran %d times for rate-limited requests", checks-before)
	}
}

func TestInstall_RateLimitFloorSurvivesACmdrdRestart(t *testing.T) {
	u, f := newTestUpdater(t)
	f.answer = false
	ts := updateServer(t, u)
	install(t, ts)
	waitJob(t, ts, JobDeclined)
	// cmdrd restarts: a new Updater (empty memory) over the same home
	u2, f2 := newTestUpdater(t)
	u2.Home = u.Home
	ts2 := updateServer(t, u2)
	code, out := install(t, ts2)
	if code != http.StatusTooManyRequests {
		t.Fatalf("after a restart: %d %v", code, out)
	}
	if f2.promptCount() != 0 {
		t.Error("a dialog was shown right after a restart")
	}
	// the file can only make it stricter: a stale one (older than the floor) does not block
	u3, _ := newTestUpdater(t)
	u3.Home = u.Home
	u3.Now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	u3.Boot()
	if why := u3.floorRefusal(); why != "" {
		t.Errorf("a prompt six minutes ago still blocks: %s", why)
	}
}

func TestInstall_DialogSaysWhereTheRequestCameFrom(t *testing.T) {
	for origin, want := range map[string]string{"app": "Requested from the Commander app.", "": "Requested via the local API", "app-ish": "Requested via the local API"} {
		u, f := newTestUpdater(t)
		f.answer = false
		ts := updateServer(t, u)
		call(t, ts, "POST", "/api/update/install", map[string]string{"action": "install"}, map[string]string{OriginHeader: origin})
		waitJob(t, ts, JobDeclined)
		if !strings.Contains(f.text, want) {
			t.Errorf("origin %q: dialog lacks %q:\n%s", origin, want, f.text)
		}
	}
}

func TestInstallDialogText_CountsHiddenUnconfirmed(t *testing.T) {
	st := updatecheck.Status{Main: mainRev, Behind: 60}
	for i := 0; i < 50; i++ {
		rev := updatecheck.NotConfirmed
		if i%2 == 0 {
			rev = updatecheck.Reviewed
		}
		st.Commits = append(st.Commits, updatecheck.Commit{Hash: mainRev, Subject: "s", Review: rev})
	}
	text := installDialogText(st)
	// commits 10..49 are hidden but known: 20 are not confirmed; 10 more are not listed at all
	if !strings.Contains(text, "and 50 more") || !strings.Contains(text, "20 of the next 40 not confirmed; the rest are not listed") {
		t.Errorf("text:\n%s", text)
	}
}

// MAJOR2: the old daemon notices a restart that did not take.
func TestRestartWatch_CommandFailureFailsTheJobWithTheWayBack(t *testing.T) {
	u, f := newTestUpdater(t)
	f.restartDone = make(chan error, 1)
	f.restartDone <- errors.New("exit status 1")
	ts := updateServer(t, u)
	install(t, ts)
	j := waitJob(t, ts, JobFailed)
	msg := j["error"].(string)
	for _, want := range []string{"restart command failed", "cmdr install --rollback", "cmdr.prev", "crash"} {
		if !strings.Contains(msg, want) {
			t.Errorf("failure text lacks %q: %s", want, msg)
		}
	}
}

func TestRestartWatch_StillHereAfterTheWatchFails(t *testing.T) {
	u, f := newTestUpdater(t)
	f.restartDone = make(chan error, 1)
	f.restartDone <- nil // the command said success, but this daemon was never replaced
	ts := updateServer(t, u)
	install(t, ts)
	j := waitJob(t, ts, JobFailed)
	if !strings.Contains(j["error"].(string), "not replaced") || !strings.Contains(j["error"].(string), "cmdr install --rollback") {
		t.Errorf("job %v", j)
	}
}

func TestRestartWatch_SettledByANewerDaemonIsNotOverwritten(t *testing.T) {
	u, f := newTestUpdater(t)
	f.restartDone = make(chan error, 1)
	ts := updateServer(t, u)
	install(t, ts)
	<-f.restart
	j := waitJob(t, ts, JobRestarting)
	// the new daemon came up and wrote done while this one still drains
	raw, _ := os.ReadFile(filepath.Join(u.Home, "update-job.json"))
	var job Job
	json.Unmarshal(raw, &job)
	job.State = JobDone
	raw, _ = json.Marshal(job)
	os.WriteFile(filepath.Join(u.Home, "update-job.json"), raw, 0o600)
	time.Sleep(500 * time.Millisecond) // past the watch
	raw, _ = os.ReadFile(filepath.Join(u.Home, "update-job.json"))
	json.Unmarshal(raw, &job)
	if job.State != JobDone || j["id"] != job.ID {
		t.Errorf("the old daemon overwrote the result: %+v", job)
	}
}

// MAJOR2: the new daemon settles only at Boot, to done when its stamp is the target.
func TestBoot_SettlesARestartingJobAndPersistsIt(t *testing.T) {
	u, _ := newTestUpdater(t)
	running := strings.TrimSuffix(updatecheck.RunningStamp(), "-dirty")
	if running == "" {
		t.Skip("test binary has no vcs stamp")
	}
	job := Job{ID: "j", Action: "install", State: JobRestarting, Target: running, Started: time.Now()}
	raw, _ := json.Marshal(job)
	os.WriteFile(filepath.Join(u.Home, "update-job.json"), raw, 0o600)
	u.Boot()
	raw, _ = os.ReadFile(filepath.Join(u.Home, "update-job.json"))
	var got Job
	json.Unmarshal(raw, &got)
	if got.State != JobDone {
		t.Errorf("persisted state = %s", got.State)
	}
}

func TestInstall_AgentRoleIsRefusedWithoutPrompting(t *testing.T) {
	u, f := newTestUpdater(t)
	ts := updateServer(t, u)
	for _, role := range []string{"operator-x", "manager-y", "commander"} {
		code, out := call(t, ts, "POST", "/api/update/install", map[string]string{}, map[string]string{RoleHeader: role})
		if code != http.StatusForbidden || !strings.Contains(out["error"].(string), "agent role") {
			t.Errorf("%s: %d %v", role, code, out)
		}
	}
	if f.promptCount() != 0 || f.got() != "" {
		t.Errorf("prompted %d, steps %s", f.promptCount(), f.got())
	}
	if req, _ := http.NewRequest("POST", ts.URL+"/api/update/install", strings.NewReader(`{}`)); true {
		if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("no token: status %d", resp.StatusCode)
		}
	}
}

func TestInstall_ConcurrentAsksOneWins(t *testing.T) {
	u, f := newTestUpdater(t)
	f.release = make(chan struct{})
	ts := updateServer(t, u)
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, _ := install(t, ts)
			codes <- c
		}()
	}
	wg.Wait()
	close(codes)
	accepted := 0
	for c := range codes {
		if c == http.StatusAccepted {
			accepted++
		}
	}
	close(f.release)
	<-f.restart
	if accepted != 1 || f.promptCount() != 1 || f.got() != "stage,install,restart" {
		t.Errorf("accepted %d, prompts %d, steps %s", accepted, f.promptCount(), f.got())
	}
}

func TestInstall_RefusalsSayWhy(t *testing.T) {
	cases := map[string]func(*fakeSteps){
		"slot":     func(f *fakeSteps) { f.busy = "a build slot is in use (ship verify)" },
		"dirty":    func(f *fakeSteps) { f.dirty = true },
		"branch":   func(f *fakeSteps) { f.branch = "op/x" },
		"uptodate": func(f *fakeSteps) { f.upToDate = true },
	}
	want := map[string]string{"slot": "build slot", "dirty": "uncommitted", "branch": "not on main", "uptodate": "up to date"}
	for name, set := range cases {
		u, f := newTestUpdater(t)
		set(f)
		ts := updateServer(t, u)
		code, out := install(t, ts)
		if code < 400 || !strings.Contains(out["error"].(string), want[name]) {
			t.Errorf("%s: %d %v", name, code, out)
		}
		if f.got() != "" || f.promptCount() != 0 {
			t.Errorf("%s: steps ran: %s, prompts %d", name, f.got(), f.promptCount())
		}
		if res := auditResults(t, u); res[len(res)-1] != "refused" {
			t.Errorf("%s: not logged as refused: %v", name, res)
		}
	}
}

func TestInstall_StageFailureStopsBeforeInstall(t *testing.T) {
	u, f := newTestUpdater(t)
	f.stageErr = errors.New("go: build failed")
	ts := updateServer(t, u)
	install(t, ts)
	j := waitJob(t, ts, JobFailed)
	if !strings.Contains(j["error"].(string), "build failed") || f.got() != "stage" {
		t.Errorf("job %v, steps %s", j, f.got())
	}
	if binstage.Revision(u.InstallDir) == mainRev {
		t.Error("installed after a failed build")
	}
}

func TestRollback_UsesTheSameConfirmedPath(t *testing.T) {
	u, f := newTestUpdater(t)
	f.prevRev = map[string]string{"cmdr.prev": "2222", "cmdrd.prev": "2222"}
	os.WriteFile(filepath.Join(u.InstallDir, "cmdr"), []byte("new"), 0o755)
	os.WriteFile(filepath.Join(u.InstallDir, "cmdrd"), []byte("new"), 0o755)
	os.WriteFile(filepath.Join(u.InstallDir, "cmdrd.prev"), []byte("old"), 0o755)
	os.WriteFile(filepath.Join(u.InstallDir, "VERSION.prev"), []byte("2222\n"), 0o644)
	ts := updateServer(t, u)
	f.answer = false
	call(t, ts, "POST", "/api/update/install", map[string]string{"action": "rollback"}, nil)
	waitJob(t, ts, JobDeclined)
	if f.got() != "" {
		t.Fatalf("rolled back without confirmation: %s", f.got())
	}
	f.answer = true
	u.Now = func() time.Time { return time.Now().Add(10 * time.Minute) } // past the rate limit
	if code, out := call(t, ts, "POST", "/api/update/install", map[string]string{"action": "rollback"}, nil); code != http.StatusAccepted {
		t.Fatalf("rollback: %d %v", code, out)
	}
	<-f.restart
	waitJob(t, ts, JobRestarting)
	if f.got() != "rollback,restart" || binstage.Revision(u.InstallDir) != "2222" {
		t.Errorf("steps %s, revision %q", f.got(), binstage.Revision(u.InstallDir))
	}
	if !strings.Contains(f.text, "2222") {
		t.Errorf("dialog does not name the target: %s", f.text)
	}
}

func TestRollback_PlantedPrevBinaryIsRefused(t *testing.T) {
	u, f := newTestUpdater(t)
	os.WriteFile(filepath.Join(u.InstallDir, "cmdrd.prev"), []byte("old"), 0o755)
	os.WriteFile(filepath.Join(u.InstallDir, "VERSION.prev"), []byte("2222\n"), 0o644)
	// VERSION.prev says 2222, but cmdrd.prev was swapped for another build.
	f.prevRev = map[string]string{"cmdr.prev": "2222", "cmdrd.prev": "6666"}
	ts := updateServer(t, u)
	code, out := call(t, ts, "POST", "/api/update/install", map[string]string{"action": "rollback"}, nil)
	if code != http.StatusConflict || !strings.Contains(fmt.Sprint(out["error"]), "cmdrd.prev") {
		t.Fatalf("planted .prev: %d %v", code, out)
	}
	if f.promptCount() != 0 || f.got() != "" {
		t.Errorf("a dialog or step ran for a planted .prev: %s", f.got())
	}
}

func TestRollback_DialogShowsRevisionsFromTheBinaries(t *testing.T) {
	u, f := newTestUpdater(t)
	os.WriteFile(filepath.Join(u.InstallDir, "cmdrd.prev"), []byte("old"), 0o755)
	os.WriteFile(filepath.Join(u.InstallDir, "VERSION.prev"), []byte("abcdef1234\n"), 0o644)
	f.prevRev = map[string]string{"cmdr.prev": "abcdef1234", "cmdrd.prev": "abcdef1234"}
	ts := updateServer(t, u)
	call(t, ts, "POST", "/api/update/install", map[string]string{"action": "rollback"}, nil)
	waitJob(t, ts, JobRestarting)
	if !strings.Contains(f.text, "abcdef1") || !strings.Contains(f.text, "read from the binaries") {
		t.Errorf("dialog: %s", f.text)
	}
}

func TestCleanLine_StripsInvisibleAndControl(t *testing.T) {
	for in, want := range map[string]string{
		"a\u2028b":              "ab",
		"fix\u202Eevil":         "fixevil",
		"zero\u200Bwidth":       "zerowidth",
		"\x1b[31mred\x1b[0m":    "[31mred[0m",
		"a\u0085b\u2029c":       "abc",
		"line one\nline  two\t": "line one line two",
	} {
		if got := cleanLine(in); got != want {
			t.Errorf("cleanLine(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("é", 150)
	if got := []rune(cleanLine(long)); len(got) != 103 {
		t.Errorf("cap: %d runes", len(got))
	}
}

func TestBoot_CleansStaleBuildDirsAndLocksParent(t *testing.T) {
	u, _ := newTestUpdater(t)
	root := filepath.Join(u.Home, "update-build")
	os.MkdirAll(filepath.Join(root, "old"), 0o755)
	os.MkdirAll(filepath.Join(root, "fresh"), 0o755)
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-2 * time.Hour)
	os.Chtimes(filepath.Join(root, "old"), past, past)
	u.Boot()
	if _, err := os.Stat(filepath.Join(root, "old")); !os.IsNotExist(err) {
		t.Error("stale build dir survived boot")
	}
	if _, err := os.Stat(filepath.Join(root, "fresh")); err != nil {
		t.Error("fresh build dir was removed")
	}
	if info, _ := os.Stat(root); info.Mode().Perm() != 0o700 {
		t.Errorf("update-build mode %v", info.Mode().Perm())
	}
}

func TestInstall_CodesignFailureIsAWarningEvent(t *testing.T) {
	u := &Updater{Home: t.TempDir()}
	u.job = &Job{}
	w := &lineWriter{u: u, step: "stage"}
	w.Write([]byte("could not sign cmdr as \"Commander Dev\"\nstaged x\n"))
	ev := u.job.Events
	if len(ev) != 2 || ev[0].Level != "warn" || ev[1].Level != "info" {
		t.Errorf("events %+v", ev)
	}
}

func TestJob_PersistedFileIsStatusOnly(t *testing.T) {
	u, f := newTestUpdater(t)
	// A file an attacker wrote: a running install with commands, a target and events.
	forged := `{"id":"j","action":"install","state":"running","target":"deadbeef","error":"; touch /tmp/x","started":"2026-01-01T00:00:00Z",` +
		`"events":[{"seq":1,"step":"restart","level":"info","text":"cmdr install --yes"}],"cmd":["touch","/tmp/pwned"]}`
	os.WriteFile(filepath.Join(u.Home, "update-job.json"), []byte(forged), 0o600)
	st := u.Status(context.Background(), false)
	if st.Job == nil || st.Job.State != JobFailed {
		t.Fatalf("job = %+v", st.Job)
	}
	if f.got() != "" || f.promptCount() != 0 {
		t.Errorf("loading the job ran something: %s", f.got())
	}
	info, err := os.Stat(filepath.Join(u.Home, "update-job.json"))
	if err != nil {
		t.Fatal(err)
	}
	_ = info
}

func TestJob_SavedWith0600(t *testing.T) {
	u, f := newTestUpdater(t)
	ts := updateServer(t, u)
	install(t, ts)
	<-f.restart
	waitJob(t, ts, JobRestarting)
	info, err := os.Stat(filepath.Join(u.Home, "update-job.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("job file: %v %v", info, err)
	}
}

func TestJob_ASecondDaemonFinishesARestartingJob(t *testing.T) {
	u, _ := newTestUpdater(t)
	job := Job{ID: "j", Action: "install", State: JobRestarting, Target: "deadbeef", Started: time.Now()}
	raw, _ := json.Marshal(job)
	os.WriteFile(filepath.Join(u.Home, "update-job.json"), raw, 0o600)
	st := u.Status(context.Background(), false)
	if st.Job == nil || st.Job.State != JobRestarting {
		t.Fatalf("job = %+v", st.Job) // not this build: still waiting
	}
	u2, _ := newTestUpdater(t)
	u2.Home = u.Home
	u2.Now = func() time.Time { return time.Now().Add(10 * time.Minute) }
	st2 := u2.Status(context.Background(), false)
	if st2.Job.State != JobFailed || !strings.Contains(st2.Job.Error, "cmdr install --rollback") {
		t.Errorf("a job whose daemon never came back = %+v", st2.Job)
	}
}

func TestUpdateCmdIsNotInTheAllowlist(t *testing.T) {
	for _, argv := range [][]string{
		{"update"}, {"update", "install"}, {"update/install"}, {"install"}, {"install", "--yes"},
		{"internal-update"}, {"internal-install"},
	} {
		if Allowed(argv) {
			t.Errorf("%v is allowed through /api/cmd", argv)
		}
	}
}

func TestUpdate_TicketRouteIsGone(t *testing.T) {
	u, _ := newTestUpdater(t)
	ts := updateServer(t, u)
	if code, _ := call(t, ts, "POST", "/api/update/ticket", map[string]string{}, nil); code == http.StatusOK || code == http.StatusAccepted {
		t.Errorf("ticket route answers %d", code)
	}
}

func TestUpdate_NoUpdaterIs503(t *testing.T) {
	ts := httptest.NewServer(New(Options{Token: "tok", Source: nopSource}).Handler())
	defer ts.Close()
	if code, _ := call(t, ts, "GET", "/api/update", nil, nil); code != http.StatusServiceUnavailable {
		t.Errorf("status %d", code)
	}
}

func nopSource(context.Context) livedata.Snapshot { return livedata.Snapshot{} }
