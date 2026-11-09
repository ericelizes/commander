package api

import (
	"bytes"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinearKeyPrivate(t *testing.T) {
	home := t.TempDir()
	src := &fakeSource{}
	src.set("test")
	s := New(Options{Source: src.load, Token: "token", ConfigPath: filepath.Join(home, "config.toml")})
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	sentinel := "sentinel-private-linear-credential"
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), sentinel) || strings.Contains(logs.String(), sentinel) {
			t.Fatal("credential leaked")
		}
		return w
	}
	for _, method := range []string{"POST", "DELETE"} {
		if w := request(method, "/api/linear/key", `{"key":"`+sentinel+`"}`, "wrong"); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	// Prime the no-key cache before saving.
	if w := request("GET", "/api/linear", "", "token"); !strings.Contains(w.Body.String(), `"key":"not set"`) {
		t.Fatal(w.Body.String())
	}
	// Even the empty-key case carries the sentinel as an allowed duplicate field.
	for _, body := range []string{
		`{"key":"` + sentinel + `","key":""}`,
		`{"key":"` + sentinel + ` "}`,
		`{"key":"` + sentinel + `\n"}`,
		`{"key":"` + sentinel + strings.Repeat("x", 4097-len(sentinel)) + `"}`,
		`{"key":"` + sentinel,
		`{"key":"` + sentinel + `","unknown":true}`,
	} {
		if w := request("POST", "/api/linear/key", body, "token"); w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	if w := request("POST", "/api/linear/key", `{"key":"`+sentinel+`"}`, "token"); w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"state":"saved"}` {
		t.Fatal(w.Code, w.Body.String())
	}
	info, err := os.Stat(filepath.Join(home, "linear.key"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	// Disable the integration locally: verifying saved status must not call Linear.
	if err := os.WriteFile(filepath.Join(home, "local.toml"), []byte("[integrations.linear]\nteam = \"\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	w := request("GET", "/api/linear", "", "token")
	if !strings.Contains(w.Body.String(), `"key":"saved"`) || strings.Contains(w.Body.String(), `"state":"no_key"`) {
		t.Fatal(w.Body.String())
	}
	if w := request("GET", "/api/snapshot", "", "token"); strings.Contains(w.Body.String(), sentinel) {
		t.Fatal("snapshot leak")
	}
	if w := request("DELETE", "/api/linear/key", "", "token"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if _, err := os.Stat(filepath.Join(home, "linear.key")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if w := request("GET", "/api/linear", "", "token"); !strings.Contains(w.Body.String(), `"key":"not set"`) {
		t.Fatal(w.Body.String())
	}
}

func TestLinearKeyWriteFailure(t *testing.T) {
	home := t.TempDir()
	// A directory at the destination forces rename to fail even as root.
	if err := os.Mkdir(filepath.Join(home, "linear.key"), 0700); err != nil {
		t.Fatal(err)
	}
	s := New(Options{Token: "token", ConfigPath: filepath.Join(home, "config.toml")})
	r := httptest.NewRequest("POST", "/api/linear/key", strings.NewReader(`{"key":"sentinel-private-linear-credential"}`))
	r.Header.Set("Authorization", "Bearer token")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 500 || strings.Contains(w.Body.String(), "sentinel-private-linear-credential") {
		t.Fatal(w.Code, w.Body.String())
	}
}
