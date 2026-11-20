package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/brief"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/overview"
	"github.com/example-user/commander/internal/store"
)

func briefRequest(t *testing.T, url, method, token string) (int, brief.Status) {
	t.Helper()
	req, _ := http.NewRequest(method, url+"/api/brief", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st brief.Status
	if resp.StatusCode == 200 {
		if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, st
}

func TestBriefEndpointServesTheLatestAndRegeneratesOnPost(t *testing.T) {
	home := t.TempDir()
	gather := func(_ context.Context, w brief.Windows) (brief.Sources, error) {
		return brief.Sources{Beads: []store.BriefBead{{Project: "p", ID: "p-ws.1", Title: "Build the retry key", Status: "closed", UpdatedAt: w.Yesterday.Start.Add(10 * time.Hour), Workstream: "p-ws", WorkstreamTitle: "Billing"}}}, nil
	}
	generate := func(_ context.Context, in brief.Input) (brief.Output, error) {
		return brief.Output{Yesterday: []brief.Group{{Workstream: "p:p-ws", Sentences: []overview.Sentence{{Text: "I built the retry key.", Sources: []string{"bead:p:p-ws.1"}}}}}}, nil
	}
	svc := brief.NewService(home, gather, generate)
	srv := httptest.NewServer(New(Options{Token: "tok", Brief: svc}).Handler())
	defer srv.Close()

	if code, _ := briefRequest(t, srv.URL, "GET", ""); code != 401 {
		t.Fatal("needs the token", code)
	}
	code, st := briefRequest(t, srv.URL, "GET", "tok")
	if code != 200 || st.Brief != nil || st.Generating || st.NextAt.IsZero() {
		t.Fatalf("%d %+v", code, st)
	}
	if code, st = briefRequest(t, srv.URL, "POST", "tok"); code != 200 {
		t.Fatal(code)
	}
	svc.Wait()
	code, st = briefRequest(t, srv.URL, "GET", "tok")
	if code != 200 || st.Brief == nil || st.Generating || len(st.Brief.Yesterday) != 1 || st.Brief.Yesterday[0].Name != "Billing" {
		t.Fatalf("%d %+v", code, st)
	}
	cited := false
	for _, e := range st.Evidence {
		cited = cited || e.ID == "bead:p:p-ws.1"
	}
	if !cited {
		t.Fatalf("the cited evidence comes with it: %+v", st.Evidence)
	}
}

func TestBriefEndpointWithoutAHomeIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(New(Options{Token: "tok"}).Handler())
	defer srv.Close()
	if code, _ := briefRequest(t, srv.URL, "GET", "tok"); code != 503 {
		t.Fatal(code)
	}
	// With a home, cmdrd builds its own Brief service: nothing written yet.
	home := t.TempDir()
	srv2 := httptest.NewServer(New(Options{Token: "tok", ConfigPath: filepath.Join(home, "config.toml")}).Handler())
	defer srv2.Close()
	if code, st := briefRequest(t, srv2.URL, "GET", "tok"); code != 200 || st.Brief != nil {
		t.Fatalf("%d %+v", code, st)
	}
}

func TestGatherBriefRefusesWithoutPostgresAndReadsWaitTimes(t *testing.T) {
	snap := livedata.Snapshot{Workstreams: []livedata.SnapWorkstream{{Project: "p", BeadID: "p-ws", Title: "Billing", Steps: []livedata.SnapStep{
		{BeadID: "p-ws.1", Children: []livedata.SnapStep{{BeadID: "p-ws.1.1", WaitUntil: 1795190400}}},
	}}}}
	s := New(Options{Token: "tok", Source: func(context.Context) livedata.Snapshot { return snap }})
	if _, err := s.gatherBrief(context.Background(), brief.WindowsAt(time.Now(), brief.Zone)); err == nil || !strings.Contains(err.Error(), "Postgres") {
		t.Fatal("a Brief without the beads would claim nothing was done:", err)
	}
	if got := waitUntils(snap); len(got) != 1 || got["p-ws.1.1"] != 1795190400 {
		t.Fatal(got)
	}
}
