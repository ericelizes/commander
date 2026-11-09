package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// `cmdr app dev` is the live-UI loop around the renderer dev server
// (`npm run dev:renderer`, Vite HMR on 127.0.0.1:5173/app/). While it answers,
// the installed app and cmdrd's /app/ load it, so an edit under app/src shows
// up in seconds with no rebuild, reinstall or restart.
//
//   cmdr app dev enable|disable|status   the always-on dev server unit
//                                        (launchd agent / systemd user unit)
//   cmdr app dev                         electron-vite dev --watch in the
//                                        foreground: also relaunches Electron
//                                        on main/preload edits (opens a window)

const appDevUsage = `usage:
  cmdr app dev enable    install + start the always-on renderer dev server (launchd / systemd --user)
  cmdr app dev disable   stop and remove it
  cmdr app dev status    unit installed? dev server answering?
  cmdr app dev           run electron-vite dev --watch here (Electron relaunches on main/preload edits)`

const (
	uiLaunchdLabel = "org.commander-ui"
	uiSystemdUnit  = "commander-ui.service"
	uiDevURL       = "http://127.0.0.1:5173/app/"
)

func runAppDevCmd(args []string, stdout, stderr io.Writer) int {
	h, err := newAppHost(stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "cmdr app dev: %v\n", err)
		return 1
	}
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "":
		err = appDevRun(h, stdout)
	case "enable":
		err = appDevEnable(h, stdout)
	case "disable":
		err = appDevDisable(h, stdout)
	case "status":
		appDevStatus(h, stdout)
	default:
		fmt.Fprintln(stderr, appDevUsage)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "cmdr app dev %s: %v\n", sub, err)
		return 1
	}
	return 0
}

func (h *appHost) appDir() string { return filepath.Join(h.repo, "app") }

// uiUnitPath is where the dev server unit goes: a LaunchAgent on macOS, a
// systemd user unit on Linux.
func (h *appHost) uiUnitPath() (string, error) {
	switch h.goos {
	case "darwin":
		return filepath.Join(h.home, "Library", "LaunchAgents", uiLaunchdLabel+".plist"), nil
	case "linux":
		cfg := h.getenv("XDG_CONFIG_HOME")
		if cfg == "" {
			cfg = filepath.Join(h.home, ".config")
		}
		return filepath.Join(cfg, "systemd", "user", uiSystemdUnit), nil
	}
	return "", fmt.Errorf("unsupported OS %q (macOS and Linux only)", h.goos)
}

func (h *appHost) uiTemplatePath() string {
	if h.goos == "darwin" {
		return filepath.Join(h.repo, "install", "launchd", uiLaunchdLabel+".plist")
	}
	return filepath.Join(h.repo, "install", "systemd", uiSystemdUnit)
}

func (h *appHost) uiLogPath() string { return filepath.Join(h.commanderHome, "logs", "ui-dev.log") }

// renderUIUnit fills the template's tokens the way install.sh does for cmdrd.
func renderUIUnit(tmpl, home, repo, nodeBin, logPath string) string {
	return strings.NewReplacer("__HOME__", home, "__REPO__", repo, "__NODE_BIN__", nodeBin, "__LOG__", logPath).Replace(tmpl)
}

func appDevEnable(h *appHost, stdout io.Writer) error {
	dest, err := h.uiUnitPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(h.appDir(), "node_modules")); err != nil {
		return fmt.Errorf("app dependencies missing: run `cd %s && npm ci` first", h.appDir())
	}
	npm, err := h.lookPath("npm")
	if err != nil {
		return errors.New("npm not on PATH")
	}
	tmpl, err := os.ReadFile(h.uiTemplatePath())
	if err != nil {
		return fmt.Errorf("unit template: %w", err)
	}
	unit := renderUIUnit(string(tmpl), h.home, h.repo, filepath.Dir(npm), h.uiLogPath())
	if err := os.MkdirAll(filepath.Dir(h.uiLogPath()), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dest, []byte(unit), 0o644); err != nil {
		return err
	}
	if h.goos == "darwin" {
		h.cleanupLegacyUI()
		domain := fmt.Sprintf("gui/%d", h.uid)
		_ = h.run("", "launchctl", "bootout", domain, dest) // a previous version, if loaded
		if err := h.run("", "launchctl", "bootstrap", domain, dest); err != nil {
			return fmt.Errorf("launchctl bootstrap %s %s: %w", domain, dest, err)
		}
	} else {
		if err := h.run("", "systemctl", "--user", "daemon-reload"); err != nil {
			return fmt.Errorf("systemctl --user daemon-reload: %w", err)
		}
		if err := h.run("", "systemctl", "--user", "enable", "--now", uiSystemdUnit); err != nil {
			return fmt.Errorf("systemctl --user enable --now %s: %w", uiSystemdUnit, err)
		}
	}
	fmt.Fprintf(stdout, "live UI dev server enabled: %s (unit %s, log %s)\n", uiDevURL, dest, h.uiLogPath())
	fmt.Fprintln(stdout, "the app and cmdrd's /app/ switch to it within a few seconds of it answering")
	return nil
}

func appDevDisable(h *appHost, stdout io.Writer) error {
	dest, err := h.uiUnitPath()
	if err != nil {
		return err
	}
	if h.goos == "darwin" {
		h.cleanupLegacyUI()
		_ = h.run("", "launchctl", "bootout", fmt.Sprintf("gui/%d", h.uid), dest)
	} else {
		_ = h.run("", "systemctl", "--user", "disable", "--now", uiSystemdUnit)
	}
	if err := os.Remove(dest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if h.goos == "linux" {
		_ = h.run("", "systemctl", "--user", "daemon-reload")
	}
	fmt.Fprintln(stdout, "live UI dev server disabled: the app shows its bundled renderer")
	return nil
}

func appDevStatus(h *appHost, stdout io.Writer) {
	dest, err := h.uiUnitPath()
	if err != nil {
		fmt.Fprintln(stdout, "unit:       ", err)
		return
	}
	installed := "not installed (cmdr app dev enable)"
	if _, err := os.Stat(dest); err == nil {
		installed = dest
	}
	answering := "no (the app shows the bundle)"
	if h.healthy(strings.TrimSuffix(uiDevURL, "/")) {
		answering = "yes (the app and /app/ load it live)"
	}
	fmt.Fprintf(stdout, "unit:        %s\n", installed)
	fmt.Fprintf(stdout, "dev server:  %s %s\n", uiDevURL, answering)
	fmt.Fprintf(stdout, "log:         %s\n", h.uiLogPath())
}

// appDevRun is the explicit, foreground full dev loop: electron-vite dev
// --watch (renderer HMR + Electron relaunch on main/preload edits). It opens
// an Electron window, so it only ever runs when asked for.
func appDevRun(h *appHost, stdout io.Writer) error {
	if _, err := os.Stat(filepath.Join(h.appDir(), "node_modules")); err != nil {
		return fmt.Errorf("app dependencies missing: run `cd %s && npm ci` first", h.appDir())
	}
	fmt.Fprintln(stdout, "electron-vite dev --watch in "+h.appDir()+" (Ctrl-C to stop)")
	return h.run(h.appDir(), "npm", "run", "dev")
}
