package evals

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example-user/commander/internal/api"
	"github.com/example-user/commander/internal/linearsync"
)

// Exercise the authenticated app read path against fake Linear; no cockpit,
// real agent, real credential or external HTTP request is involved.
func TestLinearSprintScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/linear-sprint.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Steps []struct {
			Key, Advance  bool
			Status        int
			Expected      string
			Calls, Issues int
		}
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "local.toml"), []byte("[integrations.linear]\nteam='TEAM'"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var status atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "eval-credential" {
			t.Error("authorization")
		}
		w.WriteHeader(int(status.Load()))
		io.WriteString(w, `{"data":{"teams":{"nodes":[{"activeCycle":{"name":"Cycle 46","endsAt":"2026-11-10","issues":{"nodes":[{"identifier":"TEAM-1","title":"Test sprint","url":"https://linear.app/example/issue/TEAM-1","state":{"name":"Todo","type":"unstarted"}}],"pageInfo":{"hasNextPage":false}}}}]}}}`)
	}))
	defer upstream.Close()
	now := time.Now()
	reader := linearsync.New(linearsync.Options{Home: home, Endpoint: upstream.URL, HTTP: upstream.Client().Do, Now: func() time.Time { return now }})
	server := httptest.NewServer(api.New(api.Options{Token: "eval-token", Linear: reader, ConfigPath: filepath.Join(home, "config.toml")}).Handler())
	defer server.Close()
	for i, step := range scenario.Steps {
		if step.Key && i == 1 {
			req, _ := http.NewRequest("POST", server.URL+"/api/linear/key", strings.NewReader(`{"key":"eval-credential"}`))
			req.Header.Set("Authorization", "Bearer eval-token")
			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 || strings.TrimSpace(string(body)) != `{"state":"saved"}` {
				t.Fatal(string(body))
			}
		}
		// Saving the key invalidates no_key immediately.
		if step.Advance {
			now = now.Add(linearsync.TTL)
		}
		status.Store(int32(step.Status))
		req, _ := http.NewRequest("GET", server.URL+"/api/linear", nil)
		req.Header.Set("Authorization", "Bearer eval-token")
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		var got linearsync.Response
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 || got.State != step.Expected || calls.Load() != int32(step.Calls) || len(got.Issues) != step.Issues || strings.Contains(string(body), "eval-credential") {
			t.Fatalf("step %d: %s calls=%d", i, body, calls.Load())
		}
		if (step.Expected == "bad_key" || step.Expected == "unreachable") && got.StaleSince == nil {
			t.Fatal("missing stale marker")
		}
	}
}
