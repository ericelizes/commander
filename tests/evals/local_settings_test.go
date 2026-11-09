package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/example-user/commander/internal/api"
)

// Exercise local-file changes through the authenticated app settings endpoint,
// without restarting the server or involving any live workspace or login.
func TestLocalSettingsScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/local-settings/workspace.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Steps []struct {
			Workspace, Expected string
			Remove              bool
		}
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[user]\nname = 'Alice'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.New(api.Options{ConfigPath: path, Token: "local-eval"}).Handler())
	defer server.Close()
	for _, step := range scenario.Steps {
		local := filepath.Join(dir, "local.toml")
		if step.Remove {
			err = os.Remove(local)
		} else {
			err = os.WriteFile(local, []byte(fmt.Sprintf("[integrations.linear]\nworkspace = %q\n", step.Workspace)), 0600)
		}
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), evalTimeout(t, 20*time.Second))
		t.Cleanup(cancel)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/settings", nil)
		req.Header.Set("Authorization", "Bearer local-eval")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			OK       bool
			Settings api.Settings
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 || !body.OK || body.Settings.LinearWorkspace != step.Expected {
			t.Fatalf("step %+v: status %d, body %+v, err %v", step, resp.StatusCode, body, err)
		}
	}
}
