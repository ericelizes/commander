package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/place"
	"github.com/example-user/commander/internal/store"
)

// runPlaceCaptured runs `cmdr place args...` and returns its exit, stdout, stderr.
func runPlaceCaptured(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	out, _ := os.CreateTemp(t.TempDir(), "out")
	errf, _ := os.CreateTemp(t.TempDir(), "err")
	defer out.Close()
	defer errf.Close()
	code := runPlace(args, out, errf)
	o, _ := os.ReadFile(out.Name())
	e, _ := os.ReadFile(errf.Name())
	return code, string(o), string(e)
}

func TestPlacePairRevokeList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	dev := place.NewDevice("mac:laptop")
	code, err := place.RequestPairing(home, dev.Pub, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("COMMANDER_ROLE", "manager-demo-1")
	if c, _, e := runPlaceCaptured(t, "pair", code, "--name", "laptop"); c == 0 || !strings.Contains(e, "the user's act") {
		t.Fatalf("an agent paired: %d %s", c, e)
	}
	t.Setenv("COMMANDER_ROLE", "")
	if c, _, e := runPlaceCaptured(t, "pair", code, "--name", "Bad Name"); c == 0 {
		t.Fatalf("bad name: %s", e)
	}
	if c, o, e := runPlaceCaptured(t, "pair", code, "--name", "laptop"); c != 0 || !strings.Contains(o, "paired mac:laptop") {
		t.Fatalf("pair: %d %s %s", c, o, e)
	}
	if c, _, _ := runPlaceCaptured(t, "pair", code, "--name", "laptop"); c == 0 {
		t.Fatal("a code paired twice")
	}
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := st.GetPlace(context.Background(), "mac:laptop")
	st.Close()
	if p == nil || p.PublicKey != dev.Pub || !p.Paired() {
		t.Fatalf("place %+v", p)
	}
	if c, o, _ := runPlaceCaptured(t, "list", "--json"); c != 0 || !strings.Contains(o, `"name": "mac:laptop"`) || strings.Contains(o, "token_hash\": \"x") {
		t.Fatalf("list: %s", o)
	}
	t.Setenv("COMMANDER_MANAGER_ROLE", "manager-demo-1")
	if c, _, _ := runPlaceCaptured(t, "revoke", "mac:laptop"); c == 0 {
		t.Fatal("an agent revoked")
	}
	t.Setenv("COMMANDER_MANAGER_ROLE", "")
	// Revoke stops a running run through cmdrd's stop path (the stop frame
	// reaches the Mac), and ends it in the store even when cmdrd is down.
	st, _ = store.Open(filepath.Join(home, "state.db"))
	pid, _ := st.InsertProject(context.Background(), store.Project{Name: "demo", Type: "code", Path: home, Status: "active"})
	runID, err := place.RecordOffer(context.Background(), st, place.OfferRequest{ProjectID: pid, TaskID: "demo-1.1", AgentName: "operator-demo-1.1",
		Place: "mac:laptop", StepID: "demo-1", ManagerRole: "manager-demo-1", Brief: "b", Now: time.Now()})
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	var stops []int64
	old := postPlaceStop
	postPlaceStop = func(_ context.Context, _ string, id int64, by string) error {
		stops = append(stops, id)
		return errors.New("cmdrd down")
	}
	defer func() { postPlaceStop = old }()
	if c, o, e := runPlaceCaptured(t, "revoke", "mac:laptop"); c != 0 || !strings.Contains(o, "revoked mac:laptop") || !strings.Contains(e, "heartbeat") {
		t.Fatalf("revoke: %d %s %s", c, o, e)
	}
	if len(stops) != 1 || stops[0] != runID {
		t.Fatalf("stops %v, want run %d", stops, runID)
	}
	st, _ = store.Open(filepath.Join(home, "state.db"))
	pr, _ := st.GetPlaceRun(context.Background(), runID)
	st.Close()
	if pr == nil || pr.State != store.PlaceStopped {
		t.Fatalf("run after revoke %+v", pr)
	}
	if c, _, _ := runPlaceCaptured(t, "revoke", "mac:laptop"); c == 0 {
		t.Fatal("revoked twice")
	}
}

func TestPlaceRunVerbsUseOnlyTheRunToken(t *testing.T) {
	var got []map[string]any
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Commander-Run-Token") != "tok" || r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		got = append(got, m)
		paths = append(paths, r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "bead": "demo-1.1", "to": "manager-demo-1"})
	}))
	defer srv.Close()
	runDir := placeRunDir(t, 7, srv.URL)
	t.Setenv("COMMANDER_PLACE_URL", srv.URL)
	t.Setenv("COMMANDER_PLACE_TOKEN", "tok")
	t.Setenv("COMMANDER_PLACE_RUN", "7")
	t.Setenv("COMMANDER_PLACE_DIR", runDir)

	report := filepath.Join(runDir, "report.md")
	os.WriteFile(report, []byte("Final report"), 0o600)
	if c, o, e := runPlaceCaptured(t, "report", "--file", report); c != 0 || !strings.Contains(o, "demo-1.1") {
		t.Fatalf("report: %d %s %s", c, o, e)
	}
	outside := filepath.Join(t.TempDir(), "x.md")
	os.WriteFile(outside, []byte("secret"), 0o600)
	if c, _, e := runPlaceCaptured(t, "report", "--file", outside); c == 0 || !strings.Contains(e, "run directory") {
		t.Fatalf("a file outside the run dir was posted: %s", e)
	}
	// A run directory variable pointed elsewhere (/, a home) is refused:
	// only the shape the app makes, <runs>/<run id>, private, passes.
	for _, d := range []string{"/", filepath.Dir(outside), filepath.Dir(runDir)} {
		t.Setenv("COMMANDER_PLACE_DIR", d)
		if c, _, e := runPlaceCaptured(t, "report", "--file", outside); c == 0 || !strings.Contains(e, "run directory") && !strings.Contains(e, "COMMANDER_PLACE_DIR") {
			t.Fatalf("COMMANDER_PLACE_DIR=%s posted %s: %s", d, outside, e)
		}
	}
	t.Setenv("COMMANDER_PLACE_DIR", runDir)
	if c, _, e := runPlaceCaptured(t, "comment", "--text", "halfway"); c != 0 {
		t.Fatal(e)
	}
	if c, o, _ := runPlaceCaptured(t, "mail", "--subject", "q", "--body", "b"); c != 0 || !strings.Contains(o, "manager-demo-1") {
		t.Fatal(o)
	}
	if len(paths) != 3 || paths[0] != "/api/places/runs/7/comment" || paths[2] != "/api/places/runs/7/mail" {
		t.Fatalf("paths %v", paths)
	}
	if got[0]["report"] != true || got[0]["client_id"] == "" || got[1]["report"] != nil {
		t.Fatalf("bodies %v", got)
	}

	t.Setenv("COMMANDER_PLACE_TOKEN", "")
	if c, _, e := runPlaceCaptured(t, "comment", "--text", "x"); c == 0 || !strings.Contains(e, "not inside a place run") {
		t.Fatalf("no run env: %s", e)
	}
}

// placeRunDir makes a run directory as the app does, <runs>/<id> (0700),
// with the server it recorded for the run beside it.
func placeRunDir(t *testing.T, id int64, url string) string {
	t.Helper()
	runs := filepath.Join(t.TempDir(), "runs")
	dir := filepath.Join(runs, strconv.FormatInt(id, 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(runs, strconv.FormatInt(id, 10)+".launch"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(placeURLFile(dir, id), []byte(url), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Review commander-il9.206.3.4: a COMMANDER_PLACE_URL rewritten inside the
// run (read, for) sent the run token and the report to any host. The verbs
// now talk only to the server the app recorded beside the run directory.
func TestPlaceRunVerbsTalkOnlyToTheRecordedServer(t *testing.T) {
	hits := 0
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer evil.Close()
	runDir := placeRunDir(t, 7, "http://127.0.0.1:1")
	t.Setenv("COMMANDER_PLACE_TOKEN", "tok")
	t.Setenv("COMMANDER_PLACE_RUN", "7")
	t.Setenv("COMMANDER_PLACE_DIR", runDir)
	t.Setenv("COMMANDER_PLACE_URL", evil.URL)
	if c, _, e := runPlaceCaptured(t, "comment", "--text", "x"); c == 0 || !strings.Contains(e, "not the server the app recorded") {
		t.Fatalf("a rewritten URL was used: %d %s", c, e)
	}
	// No record, or one others can write: refused too.
	os.Chmod(placeURLFile(runDir, 7), 0o644)
	t.Setenv("COMMANDER_PLACE_URL", "http://127.0.0.1:1")
	if c, _, e := runPlaceCaptured(t, "comment", "--text", "x"); c == 0 || !strings.Contains(e, "not private") {
		t.Fatalf("a writable record was trusted: %d %s", c, e)
	}
	os.Remove(placeURLFile(runDir, 7))
	if c, _, e := runPlaceCaptured(t, "comment", "--text", "x"); c == 0 || !strings.Contains(e, "missing") {
		t.Fatalf("no record: %d %s", c, e)
	}
	// The mail hook reads the same way.
	t.Setenv("COMMANDER_PLACE_URL", evil.URL)
	var out bytes.Buffer
	in, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "tool_name": "Bash"})
	runInternalMailHook([]string{"--role", "operator-op-mac", "--place"}, bytes.NewReader(in), &out)
	if hits != 0 {
		t.Fatalf("the token reached an unrecorded server %d time(s)", hits)
	}
}

func TestCheckRunOn(t *testing.T) {
	old := placeLookup
	defer func() { placeLookup = old }()
	placeLookup = func(_ context.Context, name string) (*store.Place, error) {
		switch name {
		case "mac:laptop":
			return &store.Place{Name: name}, nil
		case "mac:old":
			return &store.Place{Name: name, RevokedAt: 5}, nil
		}
		return nil, nil
	}
	for v, ok := range map[string]bool{"": true, "none": true, "mac:laptop": true, "mac:old": false, "mac:nope": false, "laptop": false} {
		if err := checkRunOn(v); (err == nil) != ok {
			t.Errorf("%q: %v", v, err)
		}
	}
	t.Setenv("COMMANDER_ROLE", "operator-x")
	if err := checkRunOn("mac:laptop"); err == nil {
		t.Fatal("an Operator chose the place")
	}
	if verb, ok := reservedLabel("run-on:mac:laptop"); !ok || !strings.Contains(verb, "--run-on") {
		t.Fatal("run-on: is not reserved")
	}
}
