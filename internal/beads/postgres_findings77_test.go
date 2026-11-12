package beads

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/store"
)

func TestPostgresFinding77OwnerRole(t *testing.T) {
	f := newQualifiedFixture(t)
	ctx := context.Background()
	role := "operator-fixture-77.1"
	if e := f.c.SetLabel(ctx, f.dir, f.source, "owner:"+OwnerLabelName(role), true); e != nil {
		t.Fatal(e)
	}
	run, e := f.s.InsertAgentRun(ctx, store.AgentRun{ProjectID: f.pid, TaskBeadID: f.source, AgentName: role, SpawnTime: time.Now().Unix()})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.link(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.DB().Exec(`INSERT INTO run_observations(run_id,alive,checked_at) VALUES(?,FALSE,?)`, run, time.Now().Unix()); e != nil {
		t.Fatal(e)
	}
	if e = f.c.Close(ctx, f.foreign, f.target); !IsCrossProjectError(e) {
		t.Fatalf("QPG-OWNER-ROLE: stopped dotted owner released blocker: %v", e)
	}
}

func TestPostgresFinding77WriteDoor(t *testing.T) {
	f := newQualifiedFixture(t)
	SetDoorServer(false)
	t.Setenv(DoorEnv, "off")
	if e := f.link(context.Background()); e == nil || !strings.Contains(e.Error(), "door") {
		t.Fatalf("QPG-WRITE-DOOR: disabled door accepted write: %v", e)
	}
	var n int
	if e := f.s.DB().QueryRow(`SELECT COUNT(*) FROM bead_dependencies WHERE project_id=? AND issue_id=?`, f.pid, f.source).Scan(&n); e != nil {
		t.Fatal(e)
	}
	if n != 0 {
		t.Fatalf("disabled door committed %d edges", n)
	}
}

func TestPostgresFinding77RunFence(t *testing.T) {
	f := newQualifiedFixture(t)
	ctx := context.Background()
	role := "operator-fixture-77"
	old, e := f.s.InsertAgentRun(ctx, store.AgentRun{ProjectID: f.pid, TaskBeadID: f.source, AgentName: role, OpencodeSessionID: "old77", SpawnTime: time.Now().Unix() - 1})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.DB().Exec(`UPDATE agent_runs SET status='done' WHERE id=?`, old); e != nil {
		t.Fatal(e)
	}
	next, e := f.s.InsertAgentRun(ctx, store.AgentRun{ProjectID: f.pid, TaskBeadID: f.source, AgentName: role, OpencodeSessionID: "new77", SpawnTime: time.Now().Unix()})
	if e != nil {
		t.Fatal(e)
	}
	actor := func(run int64, s string) context.Context {
		return WithDoorEnv(ctx, []string{"COMMANDER_ROLE=" + role, fmt.Sprint("COMMANDER_RUN_ID=", run), "COMMANDER_RUN_SESSION=" + s})
	}
	if e = f.link(actor(old, "old77")); !IsCrossProjectError(e) {
		t.Fatalf("QPG-RUN-FENCE: replaced caller authorized: %v", e)
	}
	if e = f.link(actor(next, "new77")); e != nil {
		t.Fatalf("current caller refused: %v", e)
	}
}

func TestPostgresFinding77LocalError(t *testing.T) {
	f := newQualifiedFixture(t)
	sentinel := errors.New("synthetic local reader failure")
	read := func(context.Context, *qualifiedView) error { return sentinel }
	e := withQualifiedView(context.Background(), f.dir, read)
	if !errors.Is(e, sentinel) || IsCrossProjectError(e) {
		t.Fatalf("QPG-LOCAL-ERROR: local-only error promoted: %v", e)
	}
	if e = f.link(context.Background()); e != nil {
		t.Fatal(e)
	}
	e = withQualifiedView(context.Background(), f.dir, read)
	if !errors.Is(e, sentinel) || !IsCrossProjectError(e) {
		t.Fatalf("foreign read lost refusal: %v", e)
	}
}

type finding77Counter struct {
	store.Querier
	comments int
}

func (q *finding77Counter) QueryContext(ctx context.Context, s string, args ...any) (*sql.Rows, error) {
	if strings.Contains(s, "FROM bead_comments") {
		q.comments++
	}
	return q.Querier.QueryContext(ctx, s, args...)
}
func TestPostgresFinding77DetailNPlus1(t *testing.T) {
	f := newQualifiedFixture(t)
	ctx := context.Background()
	if e := f.link(ctx); e != nil {
		t.Fatal(e)
	}
	for _, x := range []struct{ dir, id string }{{f.dir, f.source}, {f.foreign, f.target}} {
		if e := f.c.AddComment(ctx, x.dir, x.id, "comment77"); e != nil {
			t.Fatal(e)
		}
	}
	v, e := loadQualifiedView(ctx, f.s.DB(), f.pid, true)
	if e != nil {
		t.Fatal(e)
	}
	q := &finding77Counter{Querier: f.s.DB()}
	ds, e := v.details(ctx, q, []string{f.source, f.target, f.source, "missing77"})
	if e != nil {
		t.Fatal(e)
	}
	if q.comments != 1 {
		t.Fatalf("QPG-DETAIL-NPLUS1: %d comment queries, want one", q.comments)
	}
	for _, id := range []string{f.source, f.target} {
		if len(ds[id].Comments) != 1 || ds[id].Comments[0].Text != "comment77" {
			t.Fatalf("comments %s: %+v", id, ds[id])
		}
	}
}

// The eval manifest names the independent review findings and runs the same
// private database reproductions, so scenario coverage cannot silently drift.
func TestPostgresFinding77Scenario(t *testing.T) {
	raw, e := os.ReadFile("../../tests/evals/scenarios/qualified-review-findings77.json")
	if e != nil {
		t.Fatal(e)
	}
	var scenario struct {
		Findings []string `json:"findings"`
	}
	if e = json.Unmarshal(raw, &scenario); e != nil {
		t.Fatal(e)
	}
	tests := map[string]func(*testing.T){"QPG-OWNER-ROLE": TestPostgresFinding77OwnerRole, "QPG-WRITE-DOOR": TestPostgresFinding77WriteDoor, "QPG-RUN-FENCE": TestPostgresFinding77RunFence, "QPG-LOCAL-ERROR": TestPostgresFinding77LocalError, "QPG-DETAIL-NPLUS1": TestPostgresFinding77DetailNPlus1}
	if len(scenario.Findings) != len(tests) {
		t.Fatal("incomplete finding coverage")
	}
	for _, id := range scenario.Findings {
		fn, ok := tests[id]
		if !ok {
			t.Fatal(id)
		}
		t.Run(id, fn)
		delete(tests, id)
	}
}

func TestPostgresFinding77DoorTransport(t *testing.T) {
	f := newQualifiedFixture(t)
	SetDoorServer(false)
	// Missing private endpoint must refuse instead of falling back to PostgreSQL.
	if e := f.link(context.Background()); e == nil {
		t.Fatal("unavailable door accepted qualified write")
	}
	requests := make(chan DoorRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req DoorRequest
		if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		requests <- req
		// Use the real guarded PostgreSQL backend behind the transport endpoint.
		resp := postgresApplyOps(WithDoorEnv(r.Context(), req.Env), req.Dir, [][]string{req.Args})
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	for n, v := range map[string]string{"api-url": srv.URL, "api-token": "private77"} {
		if e := os.WriteFile(filepath.Join(os.Getenv("COMMANDER_HOME"), n), []byte(v), 0600); e != nil {
			t.Fatal(e)
		}
	}
	role := "operator-fixture-transport77"
	run, e := f.s.InsertAgentRun(context.Background(), store.AgentRun{ProjectID: f.pid, TaskBeadID: f.source, AgentName: role, OpencodeSessionID: "transport77", SpawnTime: time.Now().Unix()})
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("COMMANDER_ROLE", role)
	t.Setenv("COMMANDER_RUN_ID", fmt.Sprint(run))
	t.Setenv("COMMANDER_RUN_SESSION", "transport77")
	ctx := WithIntent(context.Background(), DoorIntent{Bead: f.source, Verb: "dep"})
	if e = f.link(ctx); e != nil {
		t.Fatal(e)
	}
	req := <-requests
	expected := []string{"dep", "add", f.source, f.target, "--source-project", f.dir, "--target-project", f.foreign}
	if req.Dir != f.dir || !slices.Equal(req.Args, expected) || req.Intent == nil || req.Intent.Bead != f.source {
		t.Fatalf("qualified request lost scope or intent: %+v", req)
	}
	for _, kv := range []string{"COMMANDER_ROLE=" + role, fmt.Sprint("COMMANDER_RUN_ID=", run), "COMMANDER_RUN_SESSION=transport77"} {
		if !slices.Contains(req.Env, kv) {
			t.Fatalf("lost immutable attribution %s", kv)
		}
	}
	var n int
	if e = f.s.DB().QueryRow(`SELECT COUNT(*) FROM bead_dependencies WHERE project_id=? AND issue_id=? AND target_project_id=? AND depends_on_id=?`, f.pid, f.source, f.other, f.target).Scan(&n); e != nil || n != 1 {
		t.Fatalf("backend did not commit edge: %d %v", n, e)
	}
}

func TestPostgresFinding77NativeRoleLatest(t *testing.T) {
	f := newQualifiedFixture(t)
	ctx := context.Background()
	role := "operator-fixture-native77.1"
	label := OwnerLabelName(role)
	if e := f.c.SetLabel(ctx, f.dir, f.source, "owner:"+label, true); e != nil {
		t.Fatal(e)
	}
	if e := f.link(ctx); e != nil {
		t.Fatal(e)
	}
	// Both spellings have fresh samples. The later stopped sample must win,
	// independently of the database's row order for the alias spellings.
	if _, e := f.s.DB().Exec(`INSERT INTO native_presence(role,alive,checked_at) VALUES(?,TRUE,?),(?,FALSE,?)`, label, time.Now().Unix()-5, role, time.Now().Unix()); e != nil {
		t.Fatal(e)
	}
	if e := f.c.Close(ctx, f.foreign, f.target); !IsCrossProjectError(e) {
		t.Fatalf("latest canonical native sample lost: %v", e)
	}
}
