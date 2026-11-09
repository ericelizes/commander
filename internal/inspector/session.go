package inspector

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite" // the profile's cookie DB is SQLite
)

// The persistent browser profile ($COMMANDER_HOME/browser-profile) is where
// the user signs in once (`cmdr browser login`); headless screenshots reuse it so
// they are logged in. This file answers "is there a session for host?" by
// reading the profile's cookie DB, and guards the profile with a lock.

// Site is one service `cmdr browser login` offers and `status` reports.
type Site struct {
	Name     string
	LoginURL string
	// Domains are matched against a tab's host and a cookie's host_key
	// (suffix match).
	Domains []string
	// AuthCookies, when set, are the cookie names that mean "signed in";
	// logged-out visitors get other cookies too. Empty = any cookie counts.
	AuthCookies []string
}

// Sites are the services the login page links to, in display order.
var Sites = []Site{
	{Name: "Notion", LoginURL: "https://www.notion.so/login", Domains: []string{"notion.so", "notion.com", "notion.site"}, AuthCookies: []string{"token_v2"}},
	{Name: "Linear", LoginURL: "https://linear.app/login", Domains: []string{"linear.app"}},
	{Name: "GitHub", LoginURL: "https://github.com/login", Domains: []string{"github.com"}, AuthCookies: []string{"user_session"}},
	{Name: "Datadog", LoginURL: "https://app.datadoghq.com/account/login", Domains: []string{"datadoghq.com"}, AuthCookies: []string{"dogweb"}},
	{Name: "Slack", LoginURL: "https://slack.com/signin", Domains: []string{"slack.com"}, AuthCookies: []string{"d"}},
}

// ProfileDir is the persistent profile under the Commander home dir.
func ProfileDir(commanderHome string) string {
	return filepath.Join(commanderHome, "browser-profile")
}

// profileExists reports whether dir is an existing directory.
func profileExists(dir string) bool {
	if dir == "" {
		return false
	}
	fi, err := os.Stat(dir)
	return err == nil && fi.IsDir()
}

// ProfileInUse reports whether a running Chrome holds the profile: Chrome
// keeps a SingletonLock symlink ("<host>-<pid>") in it. A lock left by a dead
// process on this host does not count (Chrome reclaims it).
func ProfileInUse(dir string) bool {
	target, err := os.Readlink(filepath.Join(dir, "SingletonLock"))
	if err != nil {
		return false
	}
	i := strings.LastIndex(target, "-")
	if i < 0 {
		return true
	}
	pid, err := strconv.Atoi(target[i+1:])
	if err != nil || pid <= 0 {
		return true
	}
	if host, _ := os.Hostname(); host != "" && target[:i] != host {
		return true // another machine's Chrome (shared home): assume live
	}
	return syscall.Kill(pid, 0) == nil
}

// LockProfile takes an exclusive flock on <profile>.lock so only one Chrome
// (headless shot or the login window's startup) uses the profile at a time.
// It polls until ctx is done; the kernel drops the lock if the holder dies,
// so a crashed shot can't wedge later ones.
func LockProfile(ctx context.Context, profile string) (unlock func(), err error) {
	f, err := os.OpenFile(profile+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, fmt.Errorf("browser profile busy")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// cookie is one row of the profile's cookie DB.
type cookie struct {
	host, name string
	expires    int64 // Chrome epoch: microseconds since 1601-01-01 UTC; 0 = session
}

// chromeEpochOffset is the seconds between 1601-01-01 and 1970-01-01.
const chromeEpochOffset = 11644473600

// ChromeTime converts t to Chrome's cookie timestamp.
func ChromeTime(t time.Time) int64 {
	return (t.Unix() + chromeEpochOffset) * 1_000_000
}

const sessionCacheTTL = 5 * time.Second

var sessionCache = struct {
	sync.Mutex
	m map[string]cachedCookies
}{m: map[string]cachedCookies{}}

type cachedCookies struct {
	at      time.Time
	cookies []cookie
}

// sessionNow is the clock for expiry checks and the cache (tests swap it).
var sessionNow = time.Now

// profileCookies returns the profile's cookies, cached for a few seconds.
func profileCookies(profile string) []cookie {
	sessionCache.Lock()
	defer sessionCache.Unlock()
	now := sessionNow()
	if c, ok := sessionCache.m[profile]; ok && now.Sub(c.at) < sessionCacheTTL {
		return c.cookies
	}
	cookies, _ := readCookies(profile)
	sessionCache.m[profile] = cachedCookies{at: now, cookies: cookies}
	return cookies
}

// cookieDBPath is the profile's cookie DB: Default/Cookies, or
// Default/Network/Cookies on Chrome builds that moved it.
func cookieDBPath(profile string) string {
	for _, p := range []string{
		filepath.Join(profile, "Default", "Cookies"),
		filepath.Join(profile, "Default", "Network", "Cookies"),
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// readCookies copies the cookie DB (Chrome keeps it locked while running)
// with its -wal / -journal to a temp dir and reads the copy.
func readCookies(profile string) ([]cookie, error) {
	src := cookieDBPath(profile)
	if src == "" {
		return nil, nil
	}
	tmp, err := os.MkdirTemp("", "cmdr-cookies-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	dst := filepath.Join(tmp, "Cookies")
	if err := copyFile(src, dst); err != nil {
		return nil, err
	}
	for _, suffix := range []string{"-wal", "-journal"} {
		_ = copyFile(src+suffix, dst+suffix) // absent is fine
	}
	db, err := sql.Open("sqlite", "file:"+dst+"?_pragma=query_only(1)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT host_key, name, expires_utc FROM cookies`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []cookie
	for rows.Next() {
		var c cookie
		if err := rows.Scan(&c.host, &c.name, &c.expires); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// domainMatch reports whether host is domain or a subdomain of it.
func domainMatch(host, domain string) bool {
	host = strings.TrimPrefix(strings.ToLower(host), ".")
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// siteFor is the known Site whose domains cover host, or a one-off Site for
// host itself.
func siteFor(host string) Site {
	for _, s := range Sites {
		for _, d := range s.Domains {
			if domainMatch(host, d) {
				return s
			}
		}
	}
	return Site{Name: host, Domains: []string{strings.ToLower(host)}}
}

// HasSession reports whether the profile has a live cookie for host's site
// (one of its auth cookies when the site has known ones). Reads are cached
// for a few seconds.
func HasSession(profileDir, host string) bool {
	if host == "" || !profileExists(profileDir) {
		return false
	}
	return siteSignedIn(profileCookies(profileDir), siteFor(host), ChromeTime(sessionNow()))
}

// SiteSignedIn is HasSession for a Site (used by `cmdr browser status`).
func SiteSignedIn(profileDir string, s Site) bool {
	if !profileExists(profileDir) {
		return false
	}
	return siteSignedIn(profileCookies(profileDir), s, ChromeTime(sessionNow()))
}

func siteSignedIn(cookies []cookie, s Site, now int64) bool {
	for _, c := range cookies {
		if c.expires != 0 && c.expires <= now {
			continue
		}
		if len(s.AuthCookies) > 0 && !contains(s.AuthCookies, c.name) {
			continue
		}
		for _, d := range s.Domains {
			if domainMatch(c.host, d) {
				return true
			}
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
