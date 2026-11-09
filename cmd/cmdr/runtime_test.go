package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/chat"
)

func TestRuntimeSwitchGoesThroughCmdrd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	var path, auth string
	var body map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.Method+" "+r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()
	os.WriteFile(filepath.Join(home, "api-url"), []byte(ts.URL+"\n"), 0o600)
	os.WriteFile(filepath.Join(home, "api-token"), []byte("tok\n"), 0o600)

	var out, errOut bytes.Buffer
	if code := runRuntime([]string{"manager-bd-1", "codex"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if path != "POST /api/chat/manager-bd-1/runtime" || auth != "Bearer tok" || body["runtime"] != "codex" {
		t.Fatalf("request = %s %q %v", path, auth, body)
	}
	if !strings.Contains(out.String(), "manager-bd-1 runs on codex") {
		t.Errorf("output: %s", out.String())
	}
}

func TestRuntimeWithCmdrdDownRecordsTheChoice(t *testing.T) {
	t.Setenv("COMMANDER_ROLE", "")
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	os.WriteFile(filepath.Join(home, "api-url"), []byte("http://127.0.0.1:1\n"), 0o600)

	if err := chat.WriteAutoRuntime(home, "operator-x", chat.RuntimeClaude); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runRuntime([]string{"operator-x", "codex"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if chat.ReadRuntime(home, "operator-x") != chat.RuntimeCodex || !strings.Contains(out.String(), "next starts") {
		t.Fatalf("not recorded: %s", out.String())
	}
	if !chat.RuntimePinned(home, "operator-x") {
		t.Fatal("explicit choice is not pinned")
	}
	out.Reset()
	if code := runRuntime([]string{"operator-x"}, &out, &errOut); code != 0 || strings.TrimSpace(out.String()) != "codex" {
		t.Fatalf("print: %d %q", code, out.String())
	}
	for _, bad := range [][]string{{}, {"nobody", "codex"}, {"operator-x", "gemini"}} {
		if code := runRuntime(bad, &out, &errOut); code != 2 {
			t.Errorf("%v: exit %d", bad, code)
		}
	}
}

func TestRuntimeAgentChoiceReachesCmdrd(t *testing.T) {
	for _, role := range []string{"commander", "manager-ws1", "operator-op-x"} {
		t.Run(role, func(t *testing.T) {
			t.Setenv("COMMANDER_ROLE", role)
			TestRuntimeSwitchGoesThroughCmdrd(t)
		})
	}
}

func TestSpawnOperator_RuntimeFlag(t *testing.T) {
	t.Setenv("COMMANDER_ROLE", "manager-ms-1")
	sp := &fakeOperatorSpawner{}
	stubSpawnDeps(t, sp, 0, 3)
	useMockBeads(t, &beads.MockClient{CreateID: "ms-1.2.1"})
	stdout, stderr, readOutput := withTempOutputFiles(t)
	code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", "ms-1.2", "--title", "Fix it", "--runtime", "codex"}, stdout, stderr)
	if _, errOut := readOutput(); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	if len(sp.reqs) != 1 || sp.reqs[0].Runtime != chat.RuntimeCodex {
		t.Fatalf("spawn requests: %+v", sp.reqs)
	}

	stdout, stderr, readOutput = withTempOutputFiles(t)
	if code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", "ms-1.2", "--title", "Fix it", "--runtime", "gemini"}, stdout, stderr); code == 0 {
		t.Fatal("unknown runtime accepted")
	}
	if _, errOut := readOutput(); !strings.Contains(errOut, "unknown --runtime") || len(sp.reqs) != 1 {
		t.Fatalf("stderr=%s reqs=%d", errOut, len(sp.reqs))
	}
}
