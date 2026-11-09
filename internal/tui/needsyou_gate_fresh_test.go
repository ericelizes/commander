package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// stubDaemon serves an empty (stale) /api/livedata and counts the requests.
func stubDaemon(t *testing.T) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "api-url"), []byte(srv.URL), 0o600)
	os.WriteFile(filepath.Join(home, "api-token"), []byte("tok"), 0o600)
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_NO_DAEMON", "")
	return &hits
}

// The fresh list must never ask cmdrd, whose snapshot lags a just-stored card;
// the plain list does (control, so the stub is proven reachable).
func TestNeedsYouGateFresh_BypassesDaemon(t *testing.T) {
	hits := stubDaemon(t)
	if _, err := ListNeedsYou(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hits.Load() == 0 {
		t.Fatal("control: ListNeedsYou should read the daemon snapshot")
	}
	hits.Store(0)
	if _, err := ListNeedsYouFresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := ListNeedsYouForBead(context.Background(), "", "commander-x.1"); err != nil {
		t.Fatal(err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("fresh lists contacted the daemon %d times", n)
	}
}
