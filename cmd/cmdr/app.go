package main

import (
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// packagedApp is where a packaged macOS build lives; when present it wins
// over the dev checkout.
const packagedApp = "/Applications/Commander.app"

// Seams for tests.
var (
	startDaemonFn = startDaemon
	launchAppFn   = launchApp
	appWait       = 10 * time.Second
)

// runApp is `cmdr app [--web]` (and `cmdr app dev ...`, app_dev.go): make sure cmdrd (and its API) is up, then open
// the desktop app, or print the /app/ link for a browser (--web, or Linux with
// no display, e.g. over an SSH port-forward).
func runApp(args []string, stdout, stderr *os.File) int {
	if len(args) > 0 && isAppInstallSubcommand(args[0]) {
		return runAppInstallCmd(args, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "shot" {
		return runAppShot(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "dev" {
		return runAppDevCmd(args[1:], stdout, stderr)
	}
	fs := flag.NewFlagSet("app", flag.ContinueOnError)
	fs.SetOutput(stderr)
	web := fs.Bool("web", false, "print the browser link to the cmdrd-served app instead of opening the desktop app")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	home := appCommanderHome()
	base, err := ensureAPI(home)
	if err != nil {
		fmt.Fprintf(stderr, "cmdr app: %v\n", err)
		return 1
	}
	if *web || !hasDisplay(runtime.GOOS, os.Getenv) {
		link, err := appWebLink(home, base)
		if err != nil {
			fmt.Fprintf(stderr, "cmdr app: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, link)
		fmt.Fprintf(stderr, "(remote box? forward it first: ssh -L %s:127.0.0.1:%s <host>, then open the link locally)\n", portOf(base), portOf(base))
		return 0
	}
	what, err := launchAppFn(home)
	if err != nil {
		fmt.Fprintf(stderr, "cmdr app: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Commander app starting (%s)\n", what)
	return 0
}

func appCommanderHome() string {
	if home := os.Getenv("COMMANDER_HOME"); home != "" {
		return home
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".commander")
}

// apiUp reports the API base URL when cmdrd answers on it.
func apiUp(home string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(home, "api-url"))
	if err != nil {
		return "", false
	}
	base := strings.TrimSpace(string(raw))
	client := http.Client{Timeout: time.Second}
	resp, err := client.Get(base + "/")
	if err != nil {
		return base, false
	}
	_ = resp.Body.Close()
	return base, resp.StatusCode == http.StatusOK
}

// ensureAPI starts cmdrd when its API does not answer and waits for it. A live
// daemon holding the lock (slow after a wake, still building its first
// snapshot) is only waited on: a second one would exit at once anyway.
func ensureAPI(home string) (string, error) {
	if base, ok := apiUp(home); ok {
		return base, nil
	}
	holder := lockHolderFn(home)
	if holder == 0 {
		if err := startDaemonFn(); err != nil {
			return "", fmt.Errorf("cmdrd is not running and could not be started: %w", err)
		}
	}
	deadline := time.Now().Add(appWait)
	for time.Now().Before(deadline) {
		if base, ok := apiUp(home); ok {
			return base, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	if holder > 0 {
		return "", fmt.Errorf("cmdrd (pid %d) is running but its API did not answer within %s", holder, appWait)
	}
	return "", fmt.Errorf("cmdrd started but its API did not answer within %s (see %s/api-url)", appWait, home)
}

// startDaemon asks the OS service manager first (the units install.sh
// installs), then falls back to running cmdrd detached.
func startDaemon() error {
	var svc *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		for _, label := range []string{appLaunchdLabel, legacyDaemonLabel} {
			if exec.Command("launchctl", "kickstart", fmt.Sprintf("gui/%d/%s", os.Getuid(), label)).Run() == nil {
				return nil
			}
		}
		return fmt.Errorf("cmdrd launchd job is not loaded; install its service unit")
	case "linux":
		svc = exec.Command("systemctl", "--user", "start", "commander.service")
	}
	if svc != nil && svc.Run() == nil {
		return nil
	}
	bin, err := exec.LookPath("cmdrd")
	if err != nil {
		return err
	}
	cmd := exec.Command(bin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// hasDisplay: macOS always has one; Linux needs X11 or Wayland.
func hasDisplay(goos string, getenv func(string) string) bool {
	if goos != "linux" {
		return true
	}
	return getenv("DISPLAY") != "" || getenv("WAYLAND_DISPLAY") != ""
}

// appWebLink is the one-time link to the cmdrd-served app: the token rides in
// the fragment, which browsers never send to a server.
func appWebLink(home, base string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(home, "api-token"))
	if err != nil {
		return "", fmt.Errorf("no API token: %w", err)
	}
	return base + "/app/#token=" + url.QueryEscape(strings.TrimSpace(string(raw))), nil
}

func portOf(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Port() == "" {
		return "8787"
	}
	return u.Port()
}

// launchApp opens the packaged app when installed (macOS), else runs
// `npm run start` in <repo>/app detached, logging to $COMMANDER_HOME/app.log.
func launchApp(home string) (string, error) {
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat(packagedApp); err == nil {
			return packagedApp, exec.Command("open", "-a", packagedApp).Run()
		}
	}
	dir := filepath.Join(resolveCommanderRepo(), "app")
	if _, err := os.Stat(filepath.Join(dir, "node_modules")); err != nil {
		return "", fmt.Errorf("app not installed: run `cd %s && npm i` first", dir)
	}
	logPath := filepath.Join(home, "app.log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return "", err
	}
	defer logf.Close()
	cmd := exec.Command("npm", "run", "start")
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	return "npm run start in " + dir + ", log " + logPath, cmd.Process.Release()
}
