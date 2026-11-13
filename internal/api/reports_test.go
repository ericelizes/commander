package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/reports"
)

func TestReportsAreServedByTheirHashNameOnly(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(t.TempDir(), "Chart p95.html")
	os.WriteFile(src, []byte("<h1>p95</h1>"), 0o644)
	name, err := reports.Import(home, src)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{ConfigPath: filepath.Join(home, "config.toml"), Token: "t"})
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	if rec := get("/reports/" + name); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "p95") {
		t.Fatalf("report %d %q", rec.Code, rec.Body)
	}
	for _, bad := range []string{"/reports/config.toml", "/reports/..%2Fconfig.toml", "/reports/" + strings.Repeat("0", 32) + ".html"} {
		if rec := get(bad); rec.Code == http.StatusOK {
			t.Fatalf("%s served", bad)
		}
	}
}
