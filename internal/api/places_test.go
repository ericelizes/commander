package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/place"
	"github.com/example-user/commander/internal/place/placetest"
	"github.com/example-user/commander/internal/store"
)

// placeServer is cmdrd's API with places on a private store, one project, an
// offered run on mac:laptop (paired through the API and the CLI's own call).
func placeServer(t *testing.T) (*httptest.Server, *place.Recorder, place.Device, int64) {
	t.Helper()
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	rec := place.NewRecorder()
	svc := place.NewService(rec.Wire(place.Deps{Store: st, Revision: "rev1"}))
	s := New(Options{Token: testToken, Source: (&fakeSource{}).load, Places: svc, PlacesHome: home})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	dev := place.NewDevice("mac:laptop")
	code, err := placetest.Pair(ts.URL, testToken, dev)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := place.TakePairing(home, code, time.Now()) // what `cmdr place pair` does
	if err != nil {
		t.Fatal(err)
	}
	st.AddPlace(ctx, store.Place{Name: dev.Place, Kind: "mac", PublicKey: pub, PairedAt: 1, PairedBy: "user"})
	pid, _ := st.InsertProject(ctx, store.Project{Name: "demo", Type: "code", Path: home, Status: "active"})
	brief := "Read and summarize."
	rec.Beads["demo-1.1"] = place.Bead{Title: "Summarize", Description: brief}
	rec.Beads["demo-1"] = place.Bead{Title: "Step", Labels: []string{place.Label(dev.Place)}}
	id, _ := st.InsertAgentRun(ctx, store.AgentRun{ProjectID: pid, TaskBeadID: "demo-1.1", AgentName: "operator-demo-1.1", SpawnTime: time.Now().Unix()})
	st.InsertPlaceRun(ctx, store.PlaceRun{RunID: id, Place: dev.Place, StepID: "demo-1", ManagerRole: "manager-demo-1", BriefSHA: place.BriefSHA(brief), OfferedAt: time.Now().Unix()})
	return ts, rec, dev, id
}

func TestPlaceChannelAndRunEndpoints(t *testing.T) {
	ts, rec, dev, runID := placeServer(t)
	if _, err := placetest.Connect(ts.URL, "wrong", dev, "rev1"); err == nil {
		t.Fatal("connected without the api token")
	}
	if _, err := placetest.Connect(ts.URL, testToken, place.NewDevice("mac:laptop"), "rev1"); err == nil {
		t.Fatal("connected with an unpaired key")
	}
	if _, err := placetest.Connect(ts.URL, testToken, dev, "rev0"); err == nil {
		t.Fatal("connected with another cmdr revision")
	}
	mac, err := placetest.Connect(ts.URL, testToken, dev, "rev1")
	if err != nil {
		t.Fatal(err)
	}
	defer mac.Close()
	off, err := mac.Next(5 * time.Second)
	if err != nil || off.Type != "offer" || off.RunID != runID {
		t.Fatalf("offer %+v %v", off, err)
	}
	run, err := mac.Ask(place.Message{Type: "fetch", RunID: runID})
	if err != nil || run.Run == nil || run.Run.Brief != "Read and summarize." {
		t.Fatalf("fetch %+v %v", run, err)
	}
	acc, err := mac.Ask(place.Message{Type: "accept", RunID: runID, BriefSHA: off.BriefSHA})
	if err != nil || acc.Token == "" {
		t.Fatalf("accept %+v %v", acc, err)
	}
	tok := acc.Token

	// The run token reaches only its run's endpoints.
	if code, _, _ := placetest.Call(ts.URL, runID, testToken, "POST", "heartbeat", map[string]any{}); code != http.StatusUnauthorized {
		t.Fatalf("api token on a run endpoint: %d", code)
	}
	if code, _, _ := placetest.Call(ts.URL, runID+1, tok, "POST", "heartbeat", map[string]any{}); code != http.StatusUnauthorized {
		t.Fatalf("token on another run: %d", code)
	}
	for _, path := range []string{"/api/snapshot", "/api/cmd", "/api/write", "/api/places/pair"} {
		req, _ := http.NewRequest("POST", ts.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set(RunTokenHeader, tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("run token reached %s: %d", path, resp.StatusCode)
		}
	}
	if code, out, _ := placetest.Call(ts.URL, runID, tok, "POST", "comment", map[string]any{"client_id": "r1", "text": "Final report", "report": true}); code != 200 || out["bead"] != "demo-1.1" {
		t.Fatalf("comment %d %v", code, out)
	}
	// "to" is not a field: the run's Manager is the only recipient.
	if code, out, _ := placetest.Call(ts.URL, runID, tok, "POST", "mail", map[string]any{"subject": "done", "body": "see bead", "to": "commander"}); code != 200 || out["to"] != "manager-demo-1" {
		t.Fatalf("mail %d %v", code, out)
	}
	if code, _, _ := placetest.Call(ts.URL, runID, tok, "GET", "mail", nil); code != 200 {
		t.Fatalf("inbox %d", code)
	}
	if code, _, _ := placetest.Call(ts.URL, runID, tok, "POST", "heartbeat", map[string]any{"alive": true}); code != 200 {
		t.Fatalf("beat %d", code)
	}
	if code, _, _ := placetest.Call(ts.URL, runID, tok, "POST", "exit", map[string]any{"status": "exited"}); code != 200 {
		t.Fatalf("exit %d", code)
	}
	if code, _, _ := placetest.Call(ts.URL, runID, tok, "POST", "heartbeat", map[string]any{}); code != http.StatusUnauthorized {
		t.Fatalf("token after exit: %d", code)
	}
	comments, mails, _ := rec.Snapshot()
	if len(comments) != 1 || comments[0].Role != "operator-demo-1.1" || len(mails) != 1 || mails[0].To != "manager-demo-1" {
		t.Fatalf("comments %+v mails %+v", comments, mails)
	}
}

func TestPlacesOffWithoutAService(t *testing.T) {
	ts := httptest.NewServer(New(Options{Token: testToken, Source: (&fakeSource{}).load}).Handler())
	defer ts.Close()
	if code, _, _ := placetest.Call(ts.URL, 1, "x", "POST", "heartbeat", map[string]any{}); code != http.StatusServiceUnavailable {
		t.Fatalf("%d", code)
	}
}

func TestDoorCommentWritesThroughTheDoor(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	var got []string
	s := New(Options{Token: testToken, Write: func(_ context.Context, _ string, args []string) ([]byte, []byte, error) {
		got = args
		return nil, nil, nil
	}})
	if err := s.DoorComment(context.Background(), dir, "demo-1.1", "", "report"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != "comments add demo-1.1 -- report" {
		t.Fatalf("args %q", got)
	}
	// Text a run sends that starts with a dash stays text (CWE-88).
	if err := s.DoorComment(context.Background(), dir, "demo-1.1", "", "--db=/x --help"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || got[3] != "--" || got[4] != "--db=/x --help" {
		t.Fatalf("args %q", got)
	}
	if err := s.DoorComment(context.Background(), t.TempDir(), "x", "", "y"); err == nil {
		t.Fatal("a dir with no .beads was written")
	}
}

func TestDoorRefusesRunOnAnUnpairedPlace(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	st.AddPlace(ctx, store.Place{Name: "mac:laptop", Kind: "mac", PublicKey: "k", PairedAt: 1, PairedBy: "user"})
	st.AddPlace(ctx, store.Place{Name: "mac:old", Kind: "mac", PublicKey: "k", PairedAt: 1, PairedBy: "user"})
	st.RevokePlace(ctx, "mac:old", 2)
	s := New(Options{Token: testToken, Places: place.NewService(place.Deps{Store: st})})
	if err := s.checkRunOnLabels(ctx, addedLabels([]string{"label", "add", "x-1", "run-on:mac:laptop"})); err != nil {
		t.Fatal(err)
	}
	for _, op := range [][]string{
		{"label", "add", "x-1", "run-on:mac:other"},
		{"update", "x-1", "--add-label", "run-on:mac:old"},
		{"create", "t", "--labels", "kind:task,run-on:mac:nope"},
	} {
		if err := s.checkRunOnLabels(ctx, addedLabels(op)); err == nil {
			t.Errorf("%v passed", op)
		}
	}
	off := New(Options{Token: testToken})
	if err := off.checkRunOnLabels(ctx, []string{"run-on:mac:laptop"}); err == nil {
		t.Fatal("no places service, label passed")
	}
}
