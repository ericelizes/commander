package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingsReadWriteAndRebalance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("[user]\nname = \"Test User\"\n"), 0o644)
	var ran [][]string
	src := &fakeSource{}
	src.set("x")
	s := New(Options{Token: testToken, Source: src.load, ConfigPath: path,
		Run: func(_ context.Context, argv []string) CmdResult {
			ran = append(ran, argv)
			return CmdResult{OK: true, Stdout: "moved 1"}
		}})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	get := func() Settings {
		req, _ := http.NewRequest("GET", ts.URL+"/api/settings", nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body struct{ Settings Settings }
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return body.Settings
	}
	if h, _ := os.Hostname(); get().Host != h {
		t.Fatalf("host = %q, want %q", get().Host, h)
	}
	if st := get(); st.UserName != "Test User" || st.CodexManagerShare != 0 || st.CodexOperatorShare != 0 { // unset: the default, nobody on Codex
		t.Fatalf("settings = %+v", st)
	}
	if err := os.WriteFile(path, []byte("[user]\nname = \"Test User\"\ntimezone = \" America/New_York \"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := get(); st.Timezone != "America/New_York" { // My Progress formats cycle dates in it
		t.Fatalf("timezone = %q", st.Timezone)
	}
	localPath := filepath.Join(filepath.Dir(path), "local.toml")
	if err := os.WriteFile(localPath, []byte("[integrations.linear]\nworkspace = 'acme'\nteam = ' TEAM '\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if st := get(); st.LinearWorkspace != "acme" || st.LinearTeam != "TEAM" {
		t.Fatalf("local settings = %+v", st)
	}
	// Reads are fresh, including removal, without restarting the daemon.
	if err := os.Remove(localPath); err != nil {
		t.Fatal(err)
	}
	if st := get(); st.LinearWorkspace != "" {
		t.Fatalf("removed local settings = %+v", st)
	}
	if code, _ := postChat(t, ts, "/api/settings/runtime", map[string]any{"codex_manager_share": 0.25, "codex_operator_share": 0.5}); code != 200 {
		t.Fatalf("write: %d", code)
	}
	if st := get(); st.CodexManagerShare != 0.25 || st.CodexOperatorShare != 0.5 {
		t.Fatalf("after write = %+v", st)
	}
	if code, _ := postChat(t, ts, "/api/settings/runtime", map[string]any{"codex_manager_share": 1.5}); code != 400 {
		t.Errorf("out of range: %d", code)
	}
	if raw, _ := os.ReadFile(path); !strings.Contains(string(raw), "name = \"Test User\"") {
		t.Errorf("config lost a line: %s", raw)
	}
	postChat(t, ts, "/api/runtime/rebalance", map[string]any{"dry_run": true})
	postChat(t, ts, "/api/runtime/rebalance", nil)
	if len(ran) != 2 || strings.Join(ran[0], " ") != "runtime rebalance --dry-run" || strings.Join(ran[1], " ") != "runtime rebalance" {
		t.Errorf("ran = %v", ran)
	}
}

func TestInvalidLocalSettingsPreserveRuntimeSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[runtime]\ncodex_manager_share = 0.25\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "local.toml"), []byte("[integrations.linear]\nworkspace = '../bad'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := New(Options{ConfigPath: path})
	got, err := s.readSettings()
	if err == nil || got.LinearWorkspace != "" || got.CodexManagerShare != 0.25 {
		t.Fatalf("settings = %+v, error %v", got, err)
	}
}
