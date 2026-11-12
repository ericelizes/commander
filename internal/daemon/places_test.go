package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/mailstate"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/place"
	"github.com/example-user/commander/internal/store"
)

// A place run that ends without a report goes back to its Manager, marked
// died, and is never restarted: a new run needs the user's dialog on the Mac.
func TestSettlePlaceNeverRestarts(t *testing.T) {
	for _, tc := range []struct {
		outcome place.Outcome
		why     string
		subject string
		body    string
	}{
		{place.Lost, "no heartbeat for 15 min", "died", "never restarted"},
		{place.Declined, "declined on the Mac", "not started", "asks the user"},
		{place.Stopped, "usage_limit", "stopped", "usage limit"},
	} {
		t.Run(string(tc.outcome), func(t *testing.T) {
			b := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "in_progress", Labels: []string{"owner:operator-x"}}}
			var mail []fullMail
			sp := &fakeSpawner{}
			s := crashSettler(b, sp, &mail)
			froze := false
			s.lockWorktree = func(string) error { froze = true; return nil }
			err := s.settlePlace(context.Background(), place.Settlement{Run: run, PlaceRun: store.PlaceRun{Place: "mac:laptop"}, Outcome: tc.outcome, Why: tc.why})
			if err != nil {
				t.Fatal(err)
			}
			if len(sp.reqs) != 0 {
				t.Fatalf("restarted: %+v", sp.reqs)
			}
			if froze {
				t.Fatal("froze a worktree a place run never had")
			}
			calls := strings.Join(b.Calls, "\n")
			if !strings.Contains(calls, "label="+beads.OperatorDiedLabel+"|on=true") {
				t.Fatalf("calls:\n%s", calls)
			}
			if len(mail) != 1 || mail[0].to != "manager-p-ws" || !strings.Contains(mail[0].subject, tc.subject) || !strings.Contains(mail[0].body, tc.body) {
				t.Fatalf("mail: %+v", mail)
			}
		})
	}
}

func TestSettlePlaceFinishedIsManagerToClose(t *testing.T) {
	b := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "in_progress", Labels: []string{"owner:operator-x"}}}
	var mail []fullMail
	s := crashSettler(b, &fakeSpawner{}, &mail)
	s.comments = func(context.Context, string, string) ([]beadComment, error) {
		return []beadComment{{Text: "Final report: three files summarized", CreatedAt: "2099-01-01T00:00:00Z"}}, nil
	}
	if err := s.settlePlace(context.Background(), place.Settlement{Run: run, PlaceRun: store.PlaceRun{Place: "mac:laptop"}, Outcome: place.Finished}); err != nil {
		t.Fatal(err)
	}
	if len(mail) != 1 || !strings.Contains(mail[0].subject, "finished") {
		t.Fatalf("mail %+v", mail)
	}
	if !strings.Contains(strings.Join(b.Calls, "\n"), "Manager_20to_20close") {
		t.Fatalf("no Manager-to-close wait: %v", b.Calls)
	}
}

// Exercise production inbox wiring against a metadata-only-unless-requested
// fake mail server. No real mail, role or external service is touched.
func TestPlaceInboxBodiesAndBacklog(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv("COMMANDER_REPO", "/invented/repository")
	t.Setenv("AM_HTTP_BEARER_TOKEN", "fake-token")
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	role := "operator-example-task"
	key := livedata.FleetMailKey()
	for r, name := range map[string]string{role: "ExampleReader", "manager-example": "ExampleSender"} {
		if err := st.UpsertAmAgent(ctx, key, r, name); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any `json:"id"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		calls++
		a := req.Params.Arguments
		if req.Params.Name != "fetch_inbox" || a["include_bodies"] != true || a["mark_read"] != false || a["agent_name"] != "ExampleReader" || a["project_key"] != key {
			t.Errorf("inbox contract: %+v", req)
		}
		mails := []map[string]any{}
		for i := 1; i <= 11; i++ {
			m := map[string]any{"id": i, "from": "ExampleSender", "subject": fmt.Sprintf("instruction %d", i), "created_ts": time.Now().Add(time.Duration(i-12) * time.Second).UTC().Format(time.RFC3339)}
			if a["include_bodies"] == true {
				m["body_md"] = fmt.Sprintf("Read invented document %d", i)
			}
			mails = append(mails, m)
		}
		b, _ := json.Marshal(mails)
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"content": []map[string]any{{"type": "text", "text": string(b)}}}})
	}))
	defer srv.Close()
	t.Setenv("AM_HTTP_URL", srv.URL)
	p := &placeDeps{st: st}
	inbox := p.deps(nil).Inbox
	mailstate.Signal(role)
	first, err := inbox(ctx, role)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 10 {
		t.Fatalf("first batch %d", len(first))
	}
	for i, m := range first {
		if m.Body != fmt.Sprintf("Read invented document %d", i+1) || m.From != "manager-example" {
			t.Fatalf("body/role lost: %+v", m)
		}
	}
	if !mailstate.NeedsCheck(role) {
		t.Fatal("backlog signal cleared")
	}
	second, err := inbox(ctx, role)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].ID != "11" || second[0].Body != "Read invented document 11" {
		t.Fatalf("undisplayed mail lost: %+v", second)
	}
	if mailstate.NeedsCheck(role) {
		t.Fatal("drained inbox still signalled")
	}
	third, err := inbox(ctx, role)
	if err != nil || len(third) != 0 || calls != 2 {
		t.Fatalf("duplicate delivery: %+v %v calls %d", third, err, calls)
	}
}
