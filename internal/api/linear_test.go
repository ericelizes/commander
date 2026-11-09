package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/example-user/commander/internal/linearsync"
)

func TestLinearAuthentication(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "local.toml"), []byte("[integrations.linear]\nteam='TEAM'"), 0600); err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(New(Options{Token: "test-token", ConfigPath: filepath.Join(home, "config.toml")}).Handler())
	defer s.Close()
	for _, token := range []string{"", "wrong", "test-token"} {
		req, _ := http.NewRequest("GET", s.URL+"/api/linear", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := s.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if token != "test-token" {
			if resp.StatusCode != 401 {
				t.Fatal(resp.StatusCode)
			}
		} else {
			var r linearsync.Response
			if err := json.NewDecoder(resp.Body).Decode(&r); err != nil || r.State != "no_key" || resp.StatusCode != 200 {
				t.Fatalf("%+v %v", r, err)
			}
		}
		resp.Body.Close()
	}
}
