package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The real templates from install/, so a broken token shows up here.
func withUITemplates(t *testing.T, f *fakeApp) {
	t.Helper()
	for _, rel := range []string{"install/launchd/org.commander-ui.plist", "install/systemd/commander-ui.service"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", rel))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(f.h.repo, rel), string(raw))
	}
	mustMkdir(t, filepath.Join(f.h.repo, "app", "node_modules"))
	f.h.lookPath = func(name string) (string, error) { return "/nvm/v24/bin/" + name, nil }
}

func devCmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runAppDevCmd(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestAppDevEnable_MacWritesAndLoadsTheLaunchAgent(t *testing.T) {
	f := newFakeApp(t, "darwin")
	withUITemplates(t, f)
	code, out, errs := devCmd(t, "enable")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	dest := filepath.Join(f.h.home, "Library", "LaunchAgents", "org.commander-ui.plist")
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	plist := string(raw)
	if strings.Contains(plist, "__") {
		t.Fatalf("unsubstituted token left:\n%s", plist)
	}
	for _, want := range []string{
		"<string>" + f.h.repo + "/app</string>",
		"npm run dev:renderer",
		"<string>/nvm/v24/bin:" + f.h.home + "/.local/bin:",
		"<string>" + filepath.Join(f.h.commanderHome, "logs", "ui-dev.log") + "</string>",
		"<key>KeepAlive</key>\n    <true/>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist lacks %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(f.h.commanderHome, "logs")); err != nil {
		t.Error("logs dir not created")
	}
	wantRan := []string{"|launchctl bootout gui/501 " + dest, "|launchctl bootstrap gui/501 " + dest}
	if strings.Join(f.ran, "\n") != strings.Join(wantRan, "\n") {
		t.Errorf("ran %v, want %v", f.ran, wantRan)
	}
	if !strings.Contains(out, "127.0.0.1:5173/app/") {
		t.Errorf("out = %q", out)
	}
}

func TestAppDevEnable_LinuxWritesAndEnablesTheUserUnit(t *testing.T) {
	f := newFakeApp(t, "linux")
	withUITemplates(t, f)
	f.env["XDG_CONFIG_HOME"] = filepath.Join(f.root, "xdg")
	if code, _, errs := devCmd(t, "enable"); code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	dest := filepath.Join(f.root, "xdg", "systemd", "user", "commander-ui.service")
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	unit := string(raw)
	for _, want := range []string{"WorkingDirectory=" + f.h.repo + "/app", "npm run dev:renderer", "Environment=PATH=/nvm/v24/bin:", "Restart=always"} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "__") {
		t.Errorf("unsubstituted token:\n%s", unit)
	}
	if want := []string{"|systemctl --user daemon-reload", "|systemctl --user enable --now commander-ui.service"}; strings.Join(f.ran, ",") != strings.Join(want, ",") {
		t.Errorf("ran %v", f.ran)
	}
}

func TestAppDevEnable_NeedsNodeModules(t *testing.T) {
	f := newFakeApp(t, "darwin")
	withUITemplates(t, f)
	os.Remove(filepath.Join(f.h.repo, "app", "node_modules"))
	code, _, errs := devCmd(t, "enable")
	if code != 1 || !strings.Contains(errs, "npm ci") {
		t.Fatalf("code %d errs %q", code, errs)
	}
	if len(f.ran) != 0 {
		t.Errorf("ran %v", f.ran)
	}
}

func TestAppDevEnable_BootstrapFailureIsAnError(t *testing.T) {
	f := newFakeApp(t, "darwin")
	withUITemplates(t, f)
	f.failRun["launchctl"] = true
	if code, _, errs := devCmd(t, "enable"); code != 1 || !strings.Contains(errs, "bootstrap") {
		t.Fatalf("code %d errs %q", code, errs)
	}
}

func TestAppDevDisable_UnloadsAndRemoves(t *testing.T) {
	f := newFakeApp(t, "darwin")
	withUITemplates(t, f)
	devCmd(t, "enable")
	f.ran = nil
	f.failRun["launchctl"] = true // not loaded: still fine
	if code, _, errs := devCmd(t, "disable"); code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	dest := filepath.Join(f.h.home, "Library", "LaunchAgents", "org.commander-ui.plist")
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Error("plist still there")
	}
	if len(f.ran) != 1 || !strings.HasPrefix(f.ran[0], "|launchctl bootout gui/501 ") {
		t.Errorf("ran %v", f.ran)
	}
}

func TestAppDevStatus_ReportsUnitAndDevServer(t *testing.T) {
	f := newFakeApp(t, "linux")
	_, out, _ := devCmd(t, "status")
	if !strings.Contains(out, "not installed") || !strings.Contains(out, "no (the app shows the bundle)") {
		t.Errorf("status = %q", out)
	}
	f.upAfter = 0
	_, out, _ = devCmd(t, "status")
	if !strings.Contains(out, "yes (the app and /app/ load it live)") {
		t.Errorf("status = %q", out)
	}
}

func TestAppDev_RunIsElectronViteDevInTheAppDir(t *testing.T) {
	f := newFakeApp(t, "darwin")
	mustMkdir(t, filepath.Join(f.h.repo, "app", "node_modules"))
	if code, _, errs := devCmd(t); code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if want := filepath.Join(f.h.repo, "app") + "|npm run dev"; len(f.ran) != 1 || f.ran[0] != want {
		t.Errorf("ran %v, want %s", f.ran, want)
	}
	if code, _, _ := devCmd(t, "bogus"); code != 2 {
		t.Errorf("unknown sub: code %d", code)
	}
}
