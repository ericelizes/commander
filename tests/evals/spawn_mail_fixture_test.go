package evals

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Spawn initializes its mail client before validating the target or gate. Give
// these refusal scenarios explicit fake credentials and a private endpoint, so
// neither startup nor failure reporting depends on the caller's mail config.
// Reject mail deliberately: the unreadable-step scenario checks the normal
// failure-mail diagnostic, while the missing-step cases must never try to mail.
func isolateSpawnMail(t *testing.T) {
	t.Helper()
	const token = "eval-spawn-mail-fixture"
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("spawn mail request did not use fixture bearer")
		}
		http.Error(w, "fixture mail unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(func() {
		server.Close()
		t.Logf("private spawn mail fixture handled %d requests", requests.Load())
	})
	t.Setenv("AM_HTTP_URL", server.URL)
	t.Setenv("AM_HTTP_BEARER_TOKEN", token)
	t.Setenv("HTTP_BEARER_TOKEN", "")
}
