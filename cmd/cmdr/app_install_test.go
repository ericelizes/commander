package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakeApp is a sandboxed appHost: every path is under a temp dir and every
// command is recorded instead of run.
type fakeApp struct {
	h        *appHost
	root     string
	env      map[string]string
	ran      []string // "dir|name args..."
	started  []string
	failRun  map[string]bool // command name -> fail
	upAfter  int             // healthy() returns true from this call on (-1: never)
	healthyN int
}

func newFakeApp(t *testing.T, goos string) *fakeApp {
	t.Helper()
	root := t.TempDir()
	f := &fakeApp{root: root, failRun: map[string]bool{}, upAfter: -1}
	f.env = map[string]string{"CMDR_APP_INSTALL_DIR": filepath.Join(root, "Applications")}
	f.h = &appHost{
		goos:          goos,
		goarch:        "arm64",
		home:          filepath.Join(root, "home"),
		commanderHome: filepath.Join(root, "commander-home"),
		repo:          filepath.Join(root, "repo"),
		uid:           501,
		getenv:        func(k string) string { return f.env[k] },
		run: func(dir, name string, args ...string) error {
			f.ran = append(f.ran, dir+"|"+strings.Join(append([]string{name}, args...), " "))
			if f.failRun[name] {
				return errors.New(name + " failed")
			}
			if name == "ditto" { // simulate the copy
				return copyTreeForTest(args[0], args[1])
			}
			return nil
		},
		start: func(name string, args ...string) error {
			f.started = append(f.started, strings.Join(append([]string{name}, args...), " "))
			return nil
		},
		healthy: func(string) bool {
			defer func() { f.healthyN++ }()
			return f.upAfter >= 0 && f.healthyN >= f.upAfter
		},
		sleep:      func(time.Duration) {},
		daemonWait: time.Second,
	}
	mustMkdir(t, f.h.commanderHome)
	writeFile(t, filepath.Join(f.h.repo, "app", "package.json"), `{"name":"commander-app","version":"1.2.3"}`)
	writeFile(t, filepath.Join(f.h.repo, "app", "build", "icons", "512x512.png"), "png")
	orig := newAppHost
	newAppHost = func(io.Writer, io.Writer) (*appHost, error) { return f.h, nil }
	t.Cleanup(func() { newAppHost = orig })
	return f
}

func (f *fakeApp) daemon(url string, upAfter int) {
	f.upAfter = upAfter
	os.WriteFile(filepath.Join(f.h.commanderHome, "api-url"), []byte(url+"\n"), 0o644)
	os.WriteFile(filepath.Join(f.h.commanderHome, "api-token"), []byte("tok123\n"), 0o600)
}

func (f *fakeApp) cmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runAppInstallCmd(args, &out, &errb)
	return code, out.String(), errb.String()
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func copyTreeForTest(src, dst string) error {
	return os.CopyFS(dst, os.DirFS(src))
}

func fakeBundle(t *testing.T, path, version string) {
	t.Helper()
	writeFile(t, filepath.Join(path, "Contents", "Info.plist"),
		"<plist><dict><key>CFBundleShortVersionString</key>\n\t<string>"+version+"</string></dict></plist>")
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestAppBuildArgs_PerOS(t *testing.T) {
	if got, want := appBuildArgs("darwin", "arm64"), []string{"run", "dist", "--", "--mac", "dir", "--arm64"}; !reflect.DeepEqual(got, want) {
		t.Errorf("darwin: %v", got)
	}
	if got, want := appBuildArgs("linux", "x64"), []string{"run", "dist", "--", "--linux", "AppImage", "--x64"}; !reflect.DeepEqual(got, want) {
		t.Errorf("linux: %v", got)
	}
}

func TestAppPaths_SwitchOnOS(t *testing.T) {
	mac := newFakeApp(t, "darwin")
	delete(mac.env, "CMDR_APP_INSTALL_DIR")
	if got := mac.h.appPath(); got != "/Applications/Commander.app" {
		t.Errorf("mac default appPath = %s", got)
	}
	lin := newFakeApp(t, "linux")
	delete(lin.env, "CMDR_APP_INSTALL_DIR")
	if got, want := lin.h.appPath(), filepath.Join(lin.h.home, "Applications", "Commander.AppImage"); got != want {
		t.Errorf("linux appPath = %s want %s", got, want)
	}
	if got, want := lin.h.desktopFile(), filepath.Join(lin.h.home, ".local", "share", "applications", "commander.desktop"); got != want {
		t.Errorf("desktop = %s", got)
	}
	lin.env["XDG_DATA_HOME"] = filepath.Join(lin.root, "xdg")
	if got := lin.h.desktopFile(); !strings.HasPrefix(got, filepath.Join(lin.root, "xdg")) {
		t.Errorf("XDG_DATA_HOME ignored: %s", got)
	}
}

func TestAppInstall_MacBuildsCopiesAndBacksUp(t *testing.T) {
	f := newFakeApp(t, "darwin")
	fakeBundle(t, filepath.Join(f.h.repo, "app", "release", "mac-arm64", "Commander.app"), "1.2.3")
	dest := f.h.appPath()
	fakeBundle(t, dest, "1.0.0") // an older install to back up

	code, out, errs := f.cmd(t, "install")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errs)
	}
	appDir := filepath.Join(f.h.repo, "app")
	for _, want := range []string{
		appDir + "|npm ci",
		appDir + "|npm run dist -- --mac dir --arm64",
		"|ditto " + filepath.Join(appDir, "release", "mac-arm64", "Commander.app") + " " + dest,
		"|" + lsregisterPath + " -f " + dest,
	} {
		if !contains(f.ran, want) {
			t.Errorf("missing command %q in %v", want, f.ran)
		}
	}
	if v := f.h.installedVersion(); v != "1.2.3" {
		t.Errorf("installed version %q", v)
	}
	if raw, err := os.ReadFile(filepath.Join(f.h.backupPath(), "Contents", "Info.plist")); err != nil || !strings.Contains(string(raw), "1.0.0") {
		t.Errorf("backup missing old bundle: %v", err)
	}
	if !strings.Contains(out, "installed "+dest+" (version 1.2.3)") || !strings.Contains(out, "previous app kept at") {
		t.Errorf("stdout: %s", out)
	}
}

func TestAppInstall_FromSkipsBuild(t *testing.T) {
	f := newFakeApp(t, "darwin")
	src := filepath.Join(f.root, "built", "Commander.app")
	fakeBundle(t, src, "9.9.9")
	if code, _, errs := f.cmd(t, "install", "--from", src); code != 0 {
		t.Fatalf("code=%d %s", code, errs)
	}
	for _, r := range f.ran {
		if strings.Contains(r, "npm") {
			t.Errorf("--from must not build: %v", f.ran)
		}
	}
}

func TestAppInstall_BuildFailureStops(t *testing.T) {
	f := newFakeApp(t, "darwin")
	f.failRun["npm"] = true
	code, _, errs := f.cmd(t, "install")
	if code != 1 || !strings.Contains(errs, "npm ci") {
		t.Fatalf("code=%d stderr=%s", code, errs)
	}
	if _, err := os.Stat(f.h.recordPath()); err == nil {
		t.Error("no record on failure")
	}
}

func TestAppInstall_LinuxAppImageDesktopEntry(t *testing.T) {
	f := newFakeApp(t, "linux")
	f.h.goarch = "amd64"
	built := filepath.Join(f.h.repo, "app", "release", "Commander-1.2.3-x86_64.AppImage")
	writeFile(t, built, "ELF")
	writeFile(t, filepath.Join(f.h.repo, "app", "release", "Commander-1.2.3-arm64.AppImage"), "wrong arch")

	if code, _, errs := f.cmd(t, "install"); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errs)
	}
	if !contains(f.ran, filepath.Join(f.h.repo, "app")+"|npm run dist -- --linux AppImage --x64") {
		t.Errorf("build args: %v", f.ran)
	}
	dest := f.h.appPath()
	fi, err := os.Stat(dest)
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("AppImage: %v %v", fi, err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "ELF" {
		t.Errorf("copied wrong arch: %q", b)
	}
	if target, err := os.Readlink(f.h.binLink()); err != nil || target != dest {
		t.Errorf("bin link: %q %v", target, err)
	}
	desk, err := os.ReadFile(f.h.desktopFile())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Exec=\"" + dest + "\" %U", "Icon=commander", "Categories=Development;", "StartupWMClass=Commander"} {
		if !strings.Contains(string(desk), want) {
			t.Errorf("desktop entry missing %q:\n%s", want, desk)
		}
	}
	if _, err := os.Stat(f.h.iconFile()); err != nil {
		t.Errorf("icon: %v", err)
	}
	if !contains(f.ran, "|update-desktop-database "+filepath.Dir(f.h.desktopFile())) {
		t.Errorf("update-desktop-database not run: %v", f.ran)
	}
	for _, r := range f.ran {
		if strings.Contains(r, "ditto") || strings.Contains(r, "lsregister") {
			t.Errorf("mac-only command on linux: %s", r)
		}
	}
	if v := f.h.installedVersion(); v != "1.2.3" {
		t.Errorf("version from record: %q", v)
	}
}

func TestAppUninstall_LinuxRemovesIntegrationKeepsBackup(t *testing.T) {
	f := newFakeApp(t, "linux")
	src := filepath.Join(f.root, "Commander.AppImage")
	writeFile(t, src, "v1")
	if code, _, errs := f.cmd(t, "install", "--from", src); code != 0 {
		t.Fatal(errs)
	}
	if code, _, errs := f.cmd(t, "install", "--from", src); code != 0 { // second install makes a backup
		t.Fatal(errs)
	}
	code, out, errs := f.cmd(t, "uninstall")
	if code != 0 {
		t.Fatal(errs)
	}
	for _, p := range []string{f.h.appPath(), f.h.binLink(), f.h.desktopFile(), f.h.iconFile(), f.h.recordPath()} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s still exists", p)
		}
	}
	if _, err := os.Stat(f.h.backupPath()); err != nil || !strings.Contains(out, "backup kept") {
		t.Errorf("backup: %v\n%s", err, out)
	}
}

func TestAppUninstall_LinuxLeavesForeignBinary(t *testing.T) {
	f := newFakeApp(t, "linux")
	writeFile(t, f.h.binLink(), "someone else's script")
	writeFile(t, f.h.appPath(), "app")
	if code, _, errs := f.cmd(t, "uninstall"); code != 0 {
		t.Fatal(errs)
	}
	if _, err := os.Stat(f.h.binLink()); err != nil {
		t.Error("uninstall removed a file it did not create")
	}
}

func TestAppStatus(t *testing.T) {
	f := newFakeApp(t, "darwin")
	_, out, _ := f.cmd(t, "status")
	for _, want := range []string{"api-url:  none", "daemon:   down", "app:      not installed"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	f.daemon("http://127.0.0.1:8787", 0)
	fakeBundle(t, f.h.appPath(), "0.4.0")
	_, out, _ = f.cmd(t, "status")
	for _, want := range []string{"api-url:  http://127.0.0.1:8787", "daemon:   up", "app:      " + f.h.appPath() + " (version 0.4.0)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "display:") {
		t.Error("macOS is never headless")
	}
}

func TestAppOpen_MacDaemonUp(t *testing.T) {
	f := newFakeApp(t, "darwin")
	f.daemon("http://127.0.0.1:8787", 0)
	fakeBundle(t, f.h.appPath(), "1.0.0")
	if code, _, errs := f.cmd(t, "open"); code != 0 {
		t.Fatal(errs)
	}
	if !reflect.DeepEqual(f.ran, []string{"|open " + f.h.appPath()}) {
		t.Errorf("ran %v", f.ran)
	}
}

func TestAppOpen_MacKickstartsDaemon(t *testing.T) {
	f := newFakeApp(t, "darwin")
	f.daemon("http://127.0.0.1:8787", 2) // down twice, then up
	fakeBundle(t, f.h.appPath(), "1.0.0")
	if code, _, errs := f.cmd(t, "open"); code != 0 {
		t.Fatal(errs)
	}
	want := []string{"|launchctl kickstart gui/501/org.commander", "|open " + f.h.appPath()}
	if !reflect.DeepEqual(f.ran, want) {
		t.Errorf("ran %v want %v", f.ran, want)
	}
	if len(f.started) != 0 {
		t.Errorf("kickstart worked; must not start cmdrd directly: %v", f.started)
	}
}

func TestAppOpen_LinuxSystemdThenFallback(t *testing.T) {
	f := newFakeApp(t, "linux")
	f.env["DISPLAY"] = ":0"
	f.daemon("http://127.0.0.1:8787", 3)
	f.failRun["systemctl"] = true
	writeFile(t, f.h.appPath(), "app")
	if code, _, errs := f.cmd(t, "open"); code != 0 {
		t.Fatal(errs)
	}
	if !contains(f.ran, "|systemctl --user start commander.service") {
		t.Errorf("ran %v", f.ran)
	}
	if !reflect.DeepEqual(f.started, []string{"cmdrd", f.h.appPath()}) {
		t.Errorf("started %v", f.started)
	}
}

func TestAppOpen_LinuxHeadlessPrintsWebURL(t *testing.T) {
	f := newFakeApp(t, "linux")
	f.daemon("http://127.0.0.1:8790/", 0)
	code, out, errs := f.cmd(t, "open")
	if code != 0 {
		t.Fatal(errs)
	}
	if strings.TrimSpace(out) != "http://127.0.0.1:8790/app/?token=tok123" {
		t.Errorf("stdout %q", out)
	}
	if len(f.started) != 0 || len(f.ran) != 0 {
		t.Errorf("headless must not launch: ran=%v started=%v", f.ran, f.started)
	}
}

func TestAppOpen_WebFlagAndDaemonDown(t *testing.T) {
	f := newFakeApp(t, "darwin")
	f.daemon("http://127.0.0.1:8787", -1)
	code, _, errs := f.cmd(t, "open", "--web")
	if code != 1 || !strings.Contains(errs, "cmdrd is not running") {
		t.Fatalf("code=%d %s", code, errs)
	}
}

func TestAppOpen_NotInstalled(t *testing.T) {
	f := newFakeApp(t, "darwin")
	f.daemon("http://127.0.0.1:8787", 0)
	code, _, errs := f.cmd(t, "open")
	if code != 1 || !strings.Contains(errs, "cmdr app install") {
		t.Fatalf("code=%d %s", code, errs)
	}
}

func TestRunAppInstallCmd_UsageAndUnsupportedOS(t *testing.T) {
	f := newFakeApp(t, "windows")
	if code, _, _ := f.cmd(t, "bogus"); code != 2 {
		t.Errorf("bogus subcommand code=%d", code)
	}
	if code, _, errs := f.cmd(t, "status"); code != 1 || !strings.Contains(errs, "unsupported OS") {
		t.Errorf("windows code=%d %s", code, errs)
	}
	for _, s := range []string{"install", "uninstall", "status", "open"} {
		if !isAppInstallSubcommand(s) {
			t.Errorf("%s should route here", s)
		}
	}
	if isAppInstallSubcommand("dev") {
		t.Error("dev belongs to app.go")
	}
}

func TestAppInstallRecordJSON(t *testing.T) {
	f := newFakeApp(t, "linux")
	src := filepath.Join(f.root, "x.AppImage")
	writeFile(t, src, "x")
	if code, _, errs := f.cmd(t, "install", "--from", src); code != 0 {
		t.Fatal(errs)
	}
	raw, _ := os.ReadFile(f.h.recordPath())
	var rec appInstallRecord
	if err := json.Unmarshal(raw, &rec); err != nil || rec.Path != f.h.appPath() || rec.Version != "1.2.3" {
		t.Errorf("record %+v %v", rec, err)
	}
}

func TestAppOpen_LinuxLiveLockHolderIsWaitedOnNotDoubled(t *testing.T) {
	f := newFakeApp(t, "linux")
	f.env["DISPLAY"] = ":0"
	f.daemon("http://127.0.0.1:8787", 3)
	f.failRun["systemctl"] = true
	writeFile(t, f.h.appPath(), "app")
	old := lockHolderFn
	lockHolderFn = func(string) int { return 4242 }
	t.Cleanup(func() { lockHolderFn = old })
	code, out, errs := f.cmd(t, "open")
	if code != 0 {
		t.Fatal(errs)
	}
	if len(f.ran) != 0 || !reflect.DeepEqual(f.started, []string{f.h.appPath()}) {
		t.Errorf("a live lock holder must not be kicked or doubled: ran=%v started=%v", f.ran, f.started)
	}
	if !strings.Contains(out, "cmdrd (pid 4242) is running but slow to answer") {
		t.Errorf("out = %q", out)
	}
}
