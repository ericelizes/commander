package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWorkflowCatalogListsEveryTemplateWithItsRules(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.serveWorkflows(rec, httptest.NewRequest(http.MethodGet, "/api/workflows", nil))
	var body struct {
		Workflows []struct {
			ID       string            `json:"id"`
			UseWhen  []string          `json:"use_when"`
			SkipWhen []string          `json:"skip_when"`
			Steps    []json.RawMessage `json:"steps"`
		} `json:"workflows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	ids := map[string]bool{}
	for _, w := range body.Workflows {
		ids[w.ID] = true
		if len(w.UseWhen) == 0 || len(w.SkipWhen) == 0 || len(w.Steps) == 0 {
			t.Fatalf("%s lacks its rules or steps", w.ID)
		}
	}
	if !ids["pr-review"] || !ids["forge-app-release"] {
		t.Fatalf("catalog %v", ids)
	}
}
