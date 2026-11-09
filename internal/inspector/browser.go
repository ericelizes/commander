package inspector

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The browser renderer shows a picture of the page: headless Chrome takes a
// screenshot sized to the pane, and the UI draws it with kitty graphics. It is
// used for generic web URLs, any tab added with `--renderer browser`, and
// Notion/Linear URLs once the persistent profile ($COMMANDER_HOME/browser-profile,
// filled by `cmdr browser login`) has a session for them. Without that profile
// each shot uses a throwaway profile and sees what a logged-out visitor sees.

const (
	browserTimeout = 20 * time.Second
	// browserLockWait caps how long a shot waits for another shot to release
	// the signed-in profile before it falls back.
	browserLockWait = 12 * time.Second

	// BrowserLoginHint / BrowserSignedInHint say whether the picture is what a
	// logged-out visitor sees or the user's signed-in view.
	BrowserLoginHint    = "headless Chrome · not logged in"
	BrowserSignedInHint = "headless Chrome · signed in"
	browserLoginOpen    = "login window open"
	browserNoGraphics   = "picture preview needs Ghostty or kitty (CMDR_GRAPHICS=kitty to force)"
	browserNoChrome     = "picture preview needs Google Chrome (or set CMDR_CHROME)"
)

// BrowserConfig turns the picture renderer on. The zero value leaves web tabs
// as link cards.
type BrowserConfig struct {
	Enabled      bool   // the terminal can draw kitty graphics
	Dir          string // where screenshots and the throwaway Chrome profile live
	Profile      string // persistent signed-in profile; used when the dir exists
	Chrome       string // Chrome binary; "" = $CMDR_CHROME, then the usual places
	CellW, CellH int    // pixels per cell
}

// chromeCandidates are tried in order when neither Chrome nor $CMDR_CHROME is set.
var chromeCandidates = []string{
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
}

func (f Fetcher) chromePath() string {
	if f.Browser.Chrome != "" {
		return f.Browser.Chrome
	}
	if p := FindHeadlessShell(f.getenv); p != "" {
		return p
	}
	return FindChrome(f.getenv)
}

// FindHeadlessShell is the chrome-headless-shell binary under
// $COMMANDER_HOME/browsers (installed with `npx @puppeteer/browsers install
// chrome-headless-shell@stable --path ~/.commander/browsers`), or "". Unlike the
// regular Chrome app it has no Dock icon and never takes focus, so screenshots
// do not pop windows up on the user's screen.
func FindHeadlessShell(getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	home := getenv("COMMANDER_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		home = filepath.Join(h, ".commander")
	}
	matches, _ := filepath.Glob(filepath.Join(home, "browsers", "chrome-headless-shell", "*", "*", "chrome-headless-shell"))
	if len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return matches[len(matches)-1]
}

// headlessFlag is "--headless=new" for the full Chrome and plain "--headless"
// for chrome-headless-shell (which is always headless).
func headlessFlag(chrome string) string {
	if strings.Contains(filepath.Base(chrome), "headless-shell") {
		return "--headless"
	}
	return "--headless=new"
}

// FindChrome is $CMDR_CHROME, else the first Chrome found in the usual places.
func FindChrome(getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	if p := getenv("CMDR_CHROME"); p != "" {
		return p
	}
	for _, c := range chromeCandidates {
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

// ShotPath is where tab target's screenshot is cached.
func ShotPath(dir, target string) string {
	sum := sha1.Sum([]byte(target))
	return filepath.Join(dir, "shots", hex.EncodeToString(sum[:6])+".png")
}

// chromeMinWidth is the narrowest layout headless Chrome does: a smaller
// --window-size still lays the page out 500px wide and crops the right side.
const chromeMinWidth = 500

// shotGeometry picks Chrome's CSS window size and device scale factor so the
// PNG is pxW x pxH device pixels (the cell box). HiDPI cells (Ghostty reports
// device pixels on a Retina screen) lay out at half the pixel width; a box
// narrower than chromeMinWidth CSS px is laid out at that width and scaled down.
func shotGeometry(pxW, pxH, cellW int) (cssW, cssH int, scale float64) {
	scale = 1
	if cellW >= 14 {
		scale = 2
	}
	if float64(pxW)/scale < chromeMinWidth {
		scale = float64(pxW) / chromeMinWidth
	}
	scale = math.Round(scale*1000) / 1000
	return int(math.Round(float64(pxW) / scale)), int(math.Round(float64(pxH) / scale)), scale
}

// browser screenshots t.Target into a card with Image set. Without kitty
// graphics or Chrome it is the link card plus a one-line hint.
func (f Fetcher) browser(ctx context.Context, run Exec, t Target) (Card, error) {
	cfg := f.Browser
	if !cfg.Enabled {
		return degrade(t, browserNoGraphics), nil
	}
	chrome := f.chromePath()
	if chrome == "" {
		return degrade(t, browserNoChrome), nil
	}
	cols, rows := t.Cols, t.Rows
	if cols <= 0 || rows <= 0 {
		cols, rows = 60, 60
	}
	cellW, cellH := cfg.CellW, cfg.CellH
	if cellW <= 0 || cellH <= 0 {
		cellW, cellH = 9, 21
	}
	cssW, cssH, scale := shotGeometry(cols*cellW, rows*cellH, cellW)

	out := ShotPath(cfg.Dir, t.Target)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return degrade(t, BrowserLoginHint), err
	}

	// The signed-in profile when there is one. One Chrome at a time may use it:
	// shots take a file lock, and a headed login window (SingletonLock) means
	// the last shot, or a logged-out one.
	hint := BrowserLoginHint
	profile := ""
	if profileExists(cfg.Profile) {
		lockCtx, cancel := context.WithTimeout(ctx, browserLockWait)
		unlock, lerr := LockProfile(lockCtx, cfg.Profile)
		cancel()
		reason := "profile busy"
		if lerr == nil {
			defer unlock()
			reason = browserLoginOpen
			if !ProfileInUse(cfg.Profile) {
				profile, reason = cfg.Profile, ""
			}
		}
		if reason != "" {
			if _, err := os.Stat(out); err == nil {
				c := LinkCard(t)
				c.Meta = []string{reason + " · showing last shot"}
				c.Image, c.ImageCols, c.ImageRows = out, cols, rows
				return c, nil
			}
			hint = BrowserLoginHint + " · " + reason
		}
	}
	if profile == "" {
		// A throwaway profile per shot: never the user's own Chrome profile, and two
		// inspector panes can shoot at once without fighting over a profile lock.
		tmpProfile, err := os.MkdirTemp(cfg.Dir, "chrome-profile-")
		if err != nil {
			return degrade(t, hint), err
		}
		defer os.RemoveAll(tmpProfile)
		profile = tmpProfile
	} else if HasSession(profile, hostOf(t.Target)) {
		hint = BrowserSignedInHint
	}
	tmp := strings.TrimSuffix(out, ".png") + ".tmp.png"
	_ = os.Remove(tmp)
	res, err := run(ctx, chrome,
		headlessFlag(chrome), "--disable-gpu", "--hide-scrollbars", "--no-first-run", "--no-default-browser-check",
		"--mute-audio", "--user-data-dir="+profile,
		"--force-device-scale-factor="+strconv.FormatFloat(scale, 'f', -1, 64),
		fmt.Sprintf("--window-size=%d,%d", cssW, cssH),
		"--screenshot="+tmp, t.Target)
	if _, statErr := os.Stat(tmp); err != nil || statErr != nil {
		if err == nil {
			err = fmt.Errorf("no screenshot written")
		}
		if ctx.Err() != nil {
			err = fmt.Errorf("timed out after %s", browserTimeout)
		}
		return degrade(t, BrowserLoginHint), fmt.Errorf("chrome screenshot: %v: %s", err, TrimTitle(string(res), 120))
	}
	if err := os.Rename(tmp, out); err != nil {
		return degrade(t, BrowserLoginHint), err
	}
	c := LinkCard(t)
	c.Meta = []string{hint}
	c.Image, c.ImageCols, c.ImageRows = out, cols, rows
	return c, nil
}

// hostOf is target's host name ("" when it is not a URL).
func hostOf(target string) string {
	u, err := url.Parse(target)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// browserFirst reports whether a Notion/Linear URL should be a picture rather
// than the text card: the terminal draws pictures, Chrome is there, and the
// signed-in profile has a session for the host.
func (f Fetcher) browserFirst(t Target) bool {
	return f.Browser.Enabled && f.Browser.Profile != "" &&
		HasSession(f.Browser.Profile, hostOf(t.Target)) && f.chromePath() != ""
}
