package evals

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example-user/commander/internal/api"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/overview"
)

func TestWorkstreamOverviewScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/workstream-overview.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Goal     string
		Evidence []overview.Evidence
		Cases    []struct{ Name, Source, Want string }
	}
	if err = json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	for _, tc := range sc.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			var writes atomic.Int32
			s := api.New(api.Options{Token: "test", Run: func(context.Context, []string) api.CmdResult { writes.Add(1); return api.CmdResult{} }, Source: func(context.Context) livedata.Snapshot {
				return livedata.Snapshot{Workstreams: []livedata.SnapWorkstream{{Project: "p", BeadID: "w", Title: sc.Goal, Steps: []livedata.SnapStep{{BeadID: "infra", Title: sc.Evidence[0].Text, Status: "open"}}}}}
			}, OverviewGenerate: func(context.Context, overview.Input) (overview.Recap, error) {
				return overview.Recap{Summary: overview.Claim{Sentences: []overview.Sentence{{Text: sc.Evidence[0].Text, Sources: []string{tc.Source}}}}}, nil
			}})
			var result overview.Result
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				req := httptest.NewRequest("POST", "/api/overview", strings.NewReader(`{"project":"p","bead_id":"w"}`))
				req.Header.Set("Authorization", "Bearer test")
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, req)
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
				if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result.State == tc.Want {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if result.State != tc.Want {
				t.Fatal(result)
			}
			if writes.Load() != 0 {
				t.Fatal("overview invoked a mutation")
			}
			if tc.Want == "retrying" && result.Recap != nil {
				t.Fatal("invented evidence reached the user")
			}
		})
	}
}
