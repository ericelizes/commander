package inspector

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fixtureCookie struct {
	host, name string
	expires    time.Time // zero = session cookie
}

// writeCookieDB fabricates <profile>/Default/Cookies like Chrome's. The DB is
// in WAL mode and stays open until the test ends, so the rows live in the -wal
// file the way they do while Chrome runs.
func writeCookieDB(t *testing.T, profile string, cookies []fixtureCookie) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(profile, "Default"), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(profile, "Default", "Cookies"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA wal_autocheckpoint=0`,
		`CREATE TABLE cookies (host_key TEXT, name TEXT, value TEXT, expires_utc INTEGER, is_persistent INTEGER)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range cookies {
		var exp int64
		if !c.expires.IsZero() {
			exp = ChromeTime(c.expires)
		}
		persistent := 0
		if exp != 0 {
			persistent = 1
		}
		if _, err := db.Exec(`INSERT INTO cookies VALUES (?, ?, '', ?, ?)`, c.host, c.name, exp, persistent); err != nil {
			t.Fatal(err)
		}
	}
}

// resetSessionCache clears the HasSession cache and pins its clock to now.
func resetSessionCache(t *testing.T, now *time.Time) {
	t.Helper()
	sessionCache.Lock()
	sessionCache.m = map[string]cachedCookies{}
	sessionCache.Unlock()
	orig := sessionNow
	sessionNow = func() time.Time { return *now }
	t.Cleanup(func() { sessionNow = orig })
}

func TestChromeTime(t *testing.T) {
	// 1970-01-01 is 11644473600 s after Chrome's 1601 epoch.
	if got := ChromeTime(time.Unix(0, 0)); got != 11644473600_000_000 {
		t.Fatalf("ChromeTime(epoch) = %d", got)
	}
}

func TestHasSession(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	resetSessionCache(t, &now)
	profile := t.TempDir()
	future, past := now.Add(30*24*time.Hour), now.Add(-time.Hour)
	writeCookieDB(t, profile, []fixtureCookie{
		{".www.notion.so", "token_v2", future},
		{".notion.so", "notion_browser_id", future},
		{".github.com", "_octo", future}, // logged-out visitors get this one too
		{"linear.app", "any", time.Time{}},
		{".slack.com", "d", past}, // expired
		{".app.datadoghq.com", "dogweb", future},
	})
	for host, want := range map[string]bool{
		"www.notion.so":     true,
		"notion.com":        true, // same site as notion.so
		"acme.notion.site":  true,
		"linear.app":        true,
		"github.com":        false, // no user_session
		"slack.com":         false, // expired
		"app.datadoghq.com": true,
		"example.com":       false,
		"":                  false,
	} {
		if got := HasSession(profile, host); got != want {
			t.Errorf("HasSession(%q) = %v, want %v", host, got, want)
		}
	}
	if HasSession(filepath.Join(profile, "missing"), "www.notion.so") {
		t.Error("missing profile has no session")
	}
	if !SiteSignedIn(profile, Sites[0]) || SiteSignedIn(profile, Sites[2]) {
		t.Error("SiteSignedIn: Notion yes, GitHub no")
	}

	// Cached for a few seconds: a vanished DB is noticed after the TTL.
	if err := os.RemoveAll(filepath.Join(profile, "Default")); err != nil {
		t.Fatal(err)
	}
	if !HasSession(profile, "linear.app") {
		t.Error("cached result expected within the TTL")
	}
	now = now.Add(sessionCacheTTL)
	if HasSession(profile, "linear.app") {
		t.Error("cache must expire")
	}
}

func TestProfileInUse(t *testing.T) {
	dir := t.TempDir()
	if ProfileInUse(dir) {
		t.Fatal("no SingletonLock: not in use")
	}
	host, _ := os.Hostname()
	lock := filepath.Join(dir, "SingletonLock")
	if err := os.Symlink(fmt.Sprintf("%s-%d", host, os.Getpid()), lock); err != nil {
		t.Fatal(err)
	}
	if !ProfileInUse(dir) {
		t.Error("live pid holds the profile")
	}
	os.Remove(lock)
	if err := os.Symlink(fmt.Sprintf("%s-%d", host, 99999999), lock); err != nil {
		t.Fatal(err)
	}
	if ProfileInUse(dir) {
		t.Error("a dead pid's lock is stale")
	}
}

func TestLockProfile_SerializesAndTimesOut(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "browser-profile")
	unlock, err := LockProfile(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := LockProfile(ctx, profile); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("second lock err = %v, want busy", err)
	}
	unlock()
	unlock2, err := LockProfile(context.Background(), profile)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	unlock2()
}
