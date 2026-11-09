package main

import (
	"context"
	"flag"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/example-user/commander/internal/inspector"
)

const browserUsage = `usage:
  cmdr browser login [--url <start url>]   Open Chrome on the inspector's profile; sign in, then quit Chrome
  cmdr browser status                      Which services the inspector's screenshots are signed in to`

// browserLaunch starts a headed Chrome and returns a func that waits for it to
// exit. Tests replace it so no real window opens.
var browserLaunch = func(chrome string, args []string) (wait func() error, err error) {
	cmd := exec.Command(chrome, args...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd.Wait, nil
}

// browserGetenv finds Chrome ($CMDR_CHROME); tests replace it.
var browserGetenv = os.Getenv

// browserStartWait is how long login keeps shots off the profile while the
// window starts (until Chrome's SingletonLock appears).
var browserStartWait = 5 * time.Second

// runBrowser is `cmdr browser login|status`.
func runBrowser(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(stderr, browserUsage)
		return 2
	}
	db, err := stateDBPath()
	if err != nil {
		fmt.Fprintf(stderr, "cmdr browser: resolve state dir: %v\n", err)
		return 1
	}
	profile := inspector.ProfileDir(filepath.Dir(db))
	switch args[0] {
	case "status":
		printBrowserStatus(stdout, profile)
		return 0
	case "login":
		return browserLogin(args[1:], profile, stdout, stderr)
	}
	fmt.Fprintf(stderr, "cmdr browser: unknown subcommand %q\n%s\n", args[0], browserUsage)
	return 2
}

func browserLogin(args []string, profile string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cmdr browser login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	startURL := fs.String("url", "", "page to open instead of the sign-in list")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := os.MkdirAll(profile, 0o700); err != nil {
		fmt.Fprintf(stderr, "cmdr browser login: %v\n", err)
		return 1
	}
	_ = os.Chmod(profile, 0o700)
	if inspector.ProfileInUse(profile) {
		fmt.Fprintf(stderr, "cmdr browser login: a Chrome already has the inspector profile open (%s).\n"+
			"Finish signing in there and quit that Chrome (⌘Q), then run `cmdr browser status`.\n", profile)
		return 1
	}
	chrome := inspector.FindChrome(browserGetenv)
	if chrome == "" {
		fmt.Fprintln(stderr, "cmdr browser login: Google Chrome not found (set CMDR_CHROME)")
		return 1
	}
	start := *startURL
	if start == "" {
		page, err := writeLoginPage()
		if err != nil {
			fmt.Fprintf(stderr, "cmdr browser login: %v\n", err)
			return 1
		}
		defer os.Remove(page)
		start = "file://" + page
	}

	// Hold the profile lock while Chrome starts so a headless shot can't grab
	// the profile in between; shots see the window's SingletonLock after that.
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	unlock, err := inspector.LockProfile(ctx, profile)
	cancel()
	if err != nil {
		fmt.Fprintf(stderr, "cmdr browser login: %v (a screenshot is using it; try again)\n", err)
		return 1
	}
	wait, err := browserLaunch(chrome, []string{
		"--user-data-dir=" + profile, "--no-first-run", "--no-default-browser-check", start,
	})
	if err != nil {
		unlock()
		fmt.Fprintf(stderr, "cmdr browser login: start chrome: %v\n", err)
		return 1
	}
	for deadline := time.Now().Add(browserStartWait); time.Now().Before(deadline) && !inspector.ProfileInUse(profile); {
		time.Sleep(100 * time.Millisecond)
	}
	unlock()
	fmt.Fprintln(stdout, "Chrome is open on the inspector's profile. Sign in, then quit Chrome (⌘Q; closing the window is not enough on macOS).")
	if err := wait(); err != nil {
		fmt.Fprintf(stderr, "cmdr browser login: chrome exited: %v\n", err)
	}
	printBrowserStatus(stdout, profile)
	return 0
}

// writeLoginPage writes the start page (links to each service's sign-in) to a
// temp file and returns its path.
func writeLoginPage() (string, error) {
	var b strings.Builder
	b.WriteString(`<!doctype html><meta charset="utf-8"><title>Commander sign-in</title>
<style>body{font:16px -apple-system,system-ui,sans-serif;max-width:32em;margin:3em auto;color:#222}
a{display:block;padding:.6em 0;font-size:1.2em}</style>
<h1>Sign in for the Commander inspector</h1>
<p>Sign in to the services you want the inspector's screenshots to see, then quit Chrome (⌘Q).</p>
`)
	for _, s := range inspector.Sites {
		fmt.Fprintf(&b, "<a href=\"%s\">%s</a>\n", html.EscapeString(s.LoginURL), html.EscapeString(s.Name))
	}
	f, err := os.CreateTemp("", "cmdr-login-*.html")
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		return "", err
	}
	return f.Name(), f.Close()
}

// printBrowserStatus prints, per service, whether the profile is signed in.
func printBrowserStatus(w io.Writer, profile string) {
	fmt.Fprintf(w, "browser profile: %s\n", profile)
	if fi, err := os.Stat(profile); err != nil || !fi.IsDir() {
		fmt.Fprintln(w, "  no profile yet: run `cmdr browser login`")
		return
	}
	if inspector.ProfileInUse(profile) {
		fmt.Fprintln(w, "  (a Chrome has it open; cookies may not be saved until it quits)")
	}
	for _, s := range inspector.Sites {
		yes := "no"
		if inspector.SiteSignedIn(profile, s) {
			yes = "yes"
		}
		fmt.Fprintf(w, "  %-8s signed in: %s\n", s.Name, yes)
	}
}
