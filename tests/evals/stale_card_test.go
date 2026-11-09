package evals

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/api"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
)

// A Manager confirms an out-of-date card through the same authenticated command
// boundary as the app, with the real cmdr binary and a br recorder: the card's
// basis is re-baselined to the step's spec and a comment is written.
func TestStaleCardScenario(t *testing.T) {
	files, _ := filepath.Glob("scenarios/stale-cards/*.json")
	if len(files) == 0 {
		t.Fatal("no stale-card scenarios")
	}
	for _, f := range files {
		t.Run(strings.TrimSuffix(filepath.Base(f), ".json"), func(t *testing.T) { runStaleCardScenario(t, f) })
	}
}

func runStaleCardScenario(t *testing.T, file string) {
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Argv          []string `json:"argv"`
		SeedSpec      string   `json:"seed_basis_spec"`
		CallsContain  []string `json:"calls_contain"`
		NoticeRenewed bool     `json:"notice_key_renewed"`
		Rebaselined   bool     `json:"basis_rebaselined"`
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_STATE_DB", "")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	pid, err := st.InsertProject(ctx, store.Project{Name: "example", Type: "code", Path: dir, Status: "active", ActivatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertNeedsYou(ctx, store.NeedsYou{ProjectID: pid, BeadID: "p-1", Ask: "Ship it", Decision: `{"question":"Ship it?","options":[{"label":"Yes"}]}`,
		Basis: &store.Basis{Spec: sc.SeedSpec, Raised: time.Now().Unix()}}); err != nil {
		t.Fatal(err)
	}
	before, err := st.GetNeedsYou(ctx, pid, "p-1")
	if err != nil {
		t.Fatal(err)
	}
	beforeKey := livedata.StaleNoticeKey(livedata.NeedsYou{Project: "example", BeadID: "p-1", Basis: before.Basis})
	script := filepath.Join(dir, "br")
	log := filepath.Join(dir, "calls")
	t.Setenv("STATE_WRITE_CALLS", log)
	t.Setenv("COMMANDER_BEADS_BIN", script)
	bead := `{"id":"p-1","title":"Ship","description":"edited later","status":"open","labels":["kind:objective"]}`
	writeExecutable(t, script, `#!/bin/sh
printf '%s\n' "$*" >> "$STATE_WRITE_CALLS"
case "$1" in
list) echo '[`+bead+`]' ;;
show)
  if [ "$3" = "--json" ]; then echo '[`+bead+`]'; else printf '○ p-1 · Ship   [OPEN]\n'; fi ;;
esac
`)
	binary := filepath.Join(dir, "cmdr")
	mustBuild(t, os.Environ(), binary, "./cmd/cmdr")
	server := httptest.NewServer(api.New(api.Options{Token: "eval", Run: api.ExecRunner(binary), Source: func(context.Context) livedata.Snapshot { return livedata.Snapshot{} }}).Handler())
	defer server.Close()
	body, _ := json.Marshal(map[string]any{"argv": sc.Argv})
	cctx, cancel := context.WithTimeout(ctx, evalTimeout(t, 20*time.Second))
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, http.MethodPost, server.URL+"/api/cmd", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer eval")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result api.CmdResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("command failed: %+v", result)
	}
	calls, _ := os.ReadFile(log)
	for _, want := range sc.CallsContain {
		if !strings.Contains(string(calls), want) {
			t.Errorf("br was not called with %q:\n%s", want, calls)
		}
	}
	got, err := st.GetNeedsYou(ctx, pid, "p-1")
	if err != nil || got == nil || got.Basis == nil {
		t.Fatalf("card after: %+v %v", got, err)
	}
	if sc.NoticeRenewed && beforeKey == livedata.StaleNoticeKey(livedata.NeedsYou{Project: "example", BeadID: "p-1", Basis: got.Basis}) {
		t.Fatal("card-check did not renew notice key")
	}
	want := beads.SpecHash(beads.Bead{Title: "Ship", Description: "edited later"})
	if sc.Rebaselined && got.Basis.Spec != want {
		t.Fatalf("basis spec = %q, want the step's current %q", got.Basis.Spec, want)
	}
}
