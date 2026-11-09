package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// `cmdr app install|uninstall|status|open` puts the packaged desktop app on
// the machine and opens it. `cmdr app` itself (dev launch) lives in app.go;
// it hands these four subcommands to runAppInstallCmd.
//
// macOS: /Applications/Commander.app, cmdrd via launchctl.
// Linux: ~/Applications/Commander.AppImage + ~/.local/bin/commander-app +
// a .desktop entry, cmdrd via `systemctl --user`. With no display (a VM over
// ssh) `open` prints the web-mode URL instead of launching Electron.

const appInstallUsage = `usage:
  cmdr app install [--skip-build] [--from <built .app or .AppImage>]
                                build the app (npm ci && npm run dist) and install it
  cmdr app uninstall            remove the installed app (the backup is kept)
  cmdr app status               api-url, daemon up/down, installed app path and version
  cmdr app open [--web]         start cmdrd if needed, then open the app
                                (--web, or no display on Linux: print the web URL)

env: CMDR_APP_INSTALL_DIR overrides /Applications (macOS) or ~/Applications (Linux).`

const (
	appLaunchdLabel = "org.commander"
	appSystemdUnit  = "commander.service"
)

// appHost is every OS-facing call the app commands make; tests replace it
// through newAppHost so nothing real is built, copied or launched.
type appHost struct {
	goos, goarch  string
	home          string // the user's home directory
	commanderHome string // $COMMANDER_HOME or ~/.commander
	repo          string // commander checkout (holds app/)
	uid           int
	getenv        func(string) string
	// run runs a command to completion (dir "" = current dir).
	run func(dir, name string, args ...string) error
	// start launches a command detached and does not wait.
	start   func(name string, args ...string) error
	healthy func(baseURL string) bool
	// lookPath finds a binary on PATH (exec.LookPath).
	lookPath func(name string) (string, error)
	sleep    func(time.Duration)
	// daemonWait bounds how long open waits for cmdrd after kicking it.
	daemonWait time.Duration
}

var newAppHost = defaultAppHost

func defaultAppHost(stdout, stderr io.Writer) (*appHost, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	db, err := stateDBPath()
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: time.Second}
	return &appHost{
		goos:          runtime.GOOS,
		goarch:        runtime.GOARCH,
		home:          home,
		commanderHome: filepath.Dir(db),
		repo:          resolveCommanderRepo(),
		uid:           os.Getuid(),
		getenv:        os.Getenv,
		run: func(dir, name string, args ...string) error {
			cmd := exec.Command(name, args...)
			cmd.Dir = dir
			cmd.Stdout, cmd.Stderr = stdout, stderr
			return cmd.Run()
		},
		start: func(name string, args ...string) error {
			cmd := exec.Command(name, args...)
			if err := cmd.Start(); err != nil {
				return err
			}
			return cmd.Process.Release()
		},
		healthy: func(baseURL string) bool {
			resp, err := client.Get(baseURL + "/")
			if err != nil {
				return false
			}
			resp.Body.Close()
			return resp.StatusCode < 500
		},
		lookPath:   exec.LookPath,
		sleep:      time.Sleep,
		daemonWait: 10 * time.Second,
	}, nil
}

// isAppInstallSubcommand reports whether `cmdr app <sub>` belongs to
// runAppInstallCmd rather than the dev launcher in app.go.
func isAppInstallSubcommand(sub string) bool {
	switch sub {
	case "install", "uninstall", "status", "open":
		return true
	}
	return false
}

// runAppInstallCmd is `cmdr app install|uninstall|status|open`; args[0] is
// the subcommand.
func runAppInstallCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || !isAppInstallSubcommand(args[0]) {
		fmt.Fprintln(stderr, appInstallUsage)
		return 2
	}
	h, err := newAppHost(stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "cmdr app %s: %v\n", args[0], err)
		return 1
	}
	if h.goos != "darwin" && h.goos != "linux" {
		fmt.Fprintf(stderr, "cmdr app %s: unsupported OS %q (macOS and Linux only)\n", args[0], h.goos)
		return 1
	}
	switch args[0] {
	case "install":
		err = appInstall(h, args[1:], stdout, stderr)
	case "uninstall":
		err = appUninstall(h, stdout)
	case "status":
		appStatus(h, stdout)
	case "open":
		err = appOpen(h, args[1:], stdout, stderr)
	}
	if errors.Is(err, flag.ErrHelp) {
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "cmdr app %s: %v\n", args[0], err)
		return 1
	}
	return 0
}

// ── paths ───────────────────────────────────────────────────────────────────

func (h *appHost) installDir() string {
	if d := h.getenv("CMDR_APP_INSTALL_DIR"); d != "" {
		return d
	}
	if h.goos == "darwin" {
		return "/Applications"
	}
	return filepath.Join(h.home, "Applications")
}

// appPath is the installed bundle (macOS) or AppImage (Linux).
func (h *appHost) appPath() string {
	if h.goos == "darwin" {
		return filepath.Join(h.installDir(), "Commander.app")
	}
	return filepath.Join(h.installDir(), "Commander.AppImage")
}

func (h *appHost) dataHome() string {
	if d := h.getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	return filepath.Join(h.home, ".local", "share")
}

func (h *appHost) desktopFile() string {
	return filepath.Join(h.dataHome(), "applications", "commander.desktop")
}

func (h *appHost) iconFile() string {
	return filepath.Join(h.dataHome(), "icons", "hicolor", "512x512", "apps", "commander.png")
}

func (h *appHost) binLink() string {
	return filepath.Join(h.home, ".local", "bin", "commander-app")
}

func (h *appHost) backupPath() string {
	return filepath.Join(h.commanderHome, "app-backup", filepath.Base(h.appPath()))
}

func (h *appHost) recordPath() string {
	return filepath.Join(h.commanderHome, "app-install.json")
}

// builderArch is electron-builder's name for the host arch.
func (h *appHost) builderArch() string {
	if h.goarch == "amd64" {
		return "x64"
	}
	return h.goarch
}

// appBuildArgs is the npm invocation that packages just what install needs
// for this host: the .app dir on macOS, the AppImage on Linux.
func appBuildArgs(goos, arch string) []string {
	if goos == "darwin" {
		return []string{"run", "dist", "--", "--mac", "dir", "--" + arch}
	}
	return []string{"run", "dist", "--", "--linux", "AppImage", "--" + arch}
}

// builtArtifact finds the packaged app under app/release for this host.
func (h *appHost) builtArtifact() (string, error) {
	release := filepath.Join(h.repo, "app", "release")
	if h.goos == "darwin" {
		dir := "mac"
		if h.goarch == "arm64" {
			dir = "mac-arm64"
		}
		p := filepath.Join(release, dir, "Commander.app")
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("no built app at %s: %w", p, err)
		}
		return p, nil
	}
	// AppImage names x64 as x86_64.
	arch := h.builderArch()
	if arch == "x64" {
		arch = "x86_64"
	}
	matches, _ := filepath.Glob(filepath.Join(release, "Commander-*-"+arch+".AppImage"))
	if len(matches) == 0 {
		return "", fmt.Errorf("no Commander-*-%s.AppImage under %s", arch, release)
	}
	return newestFile(matches), nil
}

func newestFile(paths []string) string {
	best, bestT := paths[0], time.Time{}
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && fi.ModTime().After(bestT) {
			best, bestT = p, fi.ModTime()
		}
	}
	return best
}

// ── install / uninstall ─────────────────────────────────────────────────────

type appInstallRecord struct {
	Path        string `json:"path"`
	Version     string `json:"version"`
	InstalledAt string `json:"installed_at"`
}

func appInstall(h *appHost, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("cmdr app install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	skipBuild := fs.Bool("skip-build", false, "install the last build in app/release")
	from := fs.String("from", "", "install this built .app / .AppImage instead of building")
	if err := fs.Parse(args); err != nil {
		return err
	}

	src := *from
	if src == "" {
		appDir := filepath.Join(h.repo, "app")
		if !*skipBuild {
			fmt.Fprintf(stdout, "building the app in %s\n", appDir)
			if err := h.run(appDir, "npm", "ci"); err != nil {
				return fmt.Errorf("npm ci: %w", err)
			}
			if err := h.run(appDir, "npm", appBuildArgs(h.goos, h.builderArch())...); err != nil {
				return fmt.Errorf("npm run dist: %w", err)
			}
		}
		var err error
		if src, err = h.builtArtifact(); err != nil {
			return err
		}
	}
	if _, err := os.Stat(src); err != nil {
		return err
	}

	dest := h.appPath()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := backupExisting(dest, h.backupPath()); err != nil {
		return err
	}
	if h.goos == "darwin" {
		// ditto keeps the bundle's symlinks, modes and signature intact.
		if err := h.run("", "ditto", src, dest); err != nil {
			return fmt.Errorf("copy %s: %w", src, err)
		}
		// Make Spotlight/Launchpad/`open -a Commander` see it now.
		_ = h.run("", lsregisterPath, "-f", dest)
	} else {
		if err := copyFileMode(src, dest, 0o755); err != nil {
			return err
		}
		if err := h.linuxIntegrate(dest); err != nil {
			return err
		}
	}

	rec := appInstallRecord{Path: dest, Version: h.repoAppVersion(), InstalledAt: time.Now().UTC().Format(time.RFC3339)}
	if err := writeJSONFile(h.recordPath(), rec); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "installed %s (version %s)\n", dest, orUnknown(rec.Version))
	if _, err := os.Stat(h.backupPath()); err == nil {
		fmt.Fprintf(stdout, "previous app kept at %s\n", h.backupPath())
	}
	return nil
}

const lsregisterPath = "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"

// backupExisting moves an installed app aside, replacing the previous backup.
func backupExisting(dest, backup string) error {
	if _, err := os.Lstat(dest); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
		return err
	}
	if err := os.RemoveAll(backup); err != nil {
		return err
	}
	if err := os.Rename(dest, backup); err != nil {
		return fmt.Errorf("back up %s: %w", dest, err)
	}
	return nil
}

// linuxIntegrate adds the PATH link, icon and .desktop entry.
func (h *appHost) linuxIntegrate(appImage string) error {
	link := h.binLink()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	if fi, err := os.Lstat(link); err == nil && fi.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("%s exists and is not a symlink; leaving it alone", link)
	}
	_ = os.Remove(link)
	if err := os.Symlink(appImage, link); err != nil {
		return err
	}

	icon := h.iconFile()
	if err := os.MkdirAll(filepath.Dir(icon), 0o755); err != nil {
		return err
	}
	if err := copyFileMode(filepath.Join(h.repo, "app", "build", "icons", "512x512.png"), icon, 0o644); err != nil {
		return fmt.Errorf("icon: %w", err)
	}

	desktop := h.desktopFile()
	if err := os.MkdirAll(filepath.Dir(desktop), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(desktop, []byte(desktopEntry(appImage)), 0o644); err != nil {
		return err
	}
	_ = h.run("", "update-desktop-database", filepath.Dir(desktop)) // best effort
	return nil
}

func desktopEntry(appImage string) string {
	return fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=Commander
Comment=Agent cockpit for Commander
Exec=%q %%U
Icon=commander
Terminal=false
Categories=Development;
Keywords=agents;cockpit;commander;
StartupWMClass=Commander
`, appImage)
}

func appUninstall(h *appHost, stdout io.Writer) error {
	dest := h.appPath()
	if _, err := os.Lstat(dest); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stdout, "not installed (%s)\n", dest)
	} else {
		if h.goos == "darwin" {
			_ = h.run("", lsregisterPath, "-u", dest)
		}
		if err := os.RemoveAll(dest); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "removed %s\n", dest)
	}
	if h.goos == "linux" {
		if target, err := os.Readlink(h.binLink()); err == nil && target == dest {
			_ = os.Remove(h.binLink())
		}
		for _, p := range []string{h.desktopFile(), h.iconFile()} {
			if err := os.Remove(p); err == nil {
				fmt.Fprintf(stdout, "removed %s\n", p)
			}
		}
	}
	_ = os.Remove(h.recordPath())
	if _, err := os.Stat(h.backupPath()); err == nil {
		fmt.Fprintf(stdout, "backup kept at %s\n", h.backupPath())
	}
	return nil
}

// ── status ──────────────────────────────────────────────────────────────────

func (h *appHost) apiURL() string {
	raw, err := os.ReadFile(filepath.Join(h.commanderHome, "api-url"))
	if err != nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(string(raw)), "/")
}

func (h *appHost) apiToken() string {
	raw, _ := os.ReadFile(filepath.Join(h.commanderHome, "api-token"))
	return strings.TrimSpace(string(raw))
}

func (h *appHost) daemonUp() bool {
	u := h.apiURL()
	return u != "" && h.healthy(u)
}

// headless is a Linux box with no X11/Wayland display (a VM over ssh).
func (h *appHost) headless() bool {
	return h.goos == "linux" && h.getenv("DISPLAY") == "" && h.getenv("WAYLAND_DISPLAY") == ""
}

func appStatus(h *appHost, stdout io.Writer) {
	u := h.apiURL()
	fmt.Fprintf(stdout, "api-url:  %s\n", orDefault(u, "none (cmdrd has not written "+filepath.Join(h.commanderHome, "api-url")+")"))
	daemon := "down"
	if u != "" && h.healthy(u) {
		daemon = "up"
	}
	fmt.Fprintf(stdout, "daemon:   %s\n", daemon)
	dest := h.appPath()
	if _, err := os.Stat(dest); err != nil {
		fmt.Fprintf(stdout, "app:      not installed (%s; run `cmdr app install`)\n", dest)
	} else {
		fmt.Fprintf(stdout, "app:      %s (version %s)\n", dest, orUnknown(h.installedVersion()))
	}
	if h.headless() {
		fmt.Fprintln(stdout, "display:  none (`cmdr app open` prints the web URL)")
	}
}

var plistVersionRE = regexp.MustCompile(`<key>CFBundleShortVersionString</key>\s*<string>([^<]*)</string>`)

// installedVersion reads the bundle's Info.plist on macOS, else the record
// install wrote.
func (h *appHost) installedVersion() string {
	if h.goos == "darwin" {
		raw, err := os.ReadFile(filepath.Join(h.appPath(), "Contents", "Info.plist"))
		if err == nil {
			if m := plistVersionRE.FindSubmatch(raw); m != nil {
				return string(m[1])
			}
		}
	}
	var rec appInstallRecord
	if raw, err := os.ReadFile(h.recordPath()); err == nil && json.Unmarshal(raw, &rec) == nil {
		return rec.Version
	}
	return ""
}

func (h *appHost) repoAppVersion() string {
	var pkg struct {
		Version string `json:"version"`
	}
	raw, err := os.ReadFile(filepath.Join(h.repo, "app", "package.json"))
	if err != nil || json.Unmarshal(raw, &pkg) != nil {
		return ""
	}
	return pkg.Version
}

// ── open ────────────────────────────────────────────────────────────────────

func appOpen(h *appHost, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("cmdr app open", flag.ContinueOnError)
	fs.SetOutput(stderr)
	web := fs.Bool("web", false, "print the web-mode URL instead of launching the app")
	if err := fs.Parse(args); err != nil {
		return err
	}

	up := h.ensureDaemon(stdout)
	if *web || h.headless() {
		if !up {
			return errors.New("cmdrd is not running; start it (systemctl --user start commander.service) and retry")
		}
		fmt.Fprintf(stdout, "%s/app/?token=%s\n", h.apiURL(), h.apiToken())
		return nil
	}
	if !up {
		fmt.Fprintln(stderr, "cmdr app open: cmdrd is still down; the app will show offline and keep retrying")
	}

	dest := h.appPath()
	if _, err := os.Stat(dest); err != nil {
		return fmt.Errorf("app not installed at %s; run `cmdr app install`", dest)
	}
	if h.goos == "darwin" {
		return h.run("", "open", dest)
	}
	return h.start(dest)
}

// ensureDaemon returns whether cmdrd answers, kicking its service unit (and,
// failing that, starting cmdrd directly) unless a live cmdrd holds the lock,
// and waiting up to daemonWait.
func (h *appHost) ensureDaemon(stdout io.Writer) bool {
	if h.daemonUp() {
		return true
	}
	if pid := lockHolderFn(h.commanderHome); pid > 0 {
		fmt.Fprintf(stdout, "cmdrd (pid %d) is running but slow to answer; waiting\n", pid)
	} else {
		fmt.Fprintln(stdout, "cmdrd is down; starting it")
		var err error
		if h.goos == "darwin" {
			err = h.run("", "launchctl", "kickstart", "gui/"+strconv.Itoa(h.uid)+"/"+appLaunchdLabel)
			if err != nil {
				err = h.run("", "launchctl", "kickstart", "gui/"+strconv.Itoa(h.uid)+"/"+legacyDaemonLabel)
			}
		} else {
			err = h.run("", "systemctl", "--user", "start", appSystemdUnit)
		}
		if err != nil && h.goos != "darwin" {
			_ = h.start("cmdrd")
		}
	}
	const step = 250 * time.Millisecond
	for waited := time.Duration(0); waited < h.daemonWait; waited += step {
		h.sleep(step)
		if h.daemonUp() {
			return true
		}
	}
	return false
}

// ── small helpers ───────────────────────────────────────────────────────────

func copyFileMode(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func writeJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

func orUnknown(s string) string { return orDefault(s, "unknown") }

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
