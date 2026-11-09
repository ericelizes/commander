package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// appShotWait covers cmdrd's own 10s wait for the app plus slack.
var appShotWait = 20 * time.Second

// runAppShot is `cmdr app shot [--text-stdout]`: ask cmdrd for a picture of the
// open Commander app window (the app captures itself, so no Screen Recording
// permission). Read-only; it never starts the daemon or the app. The capture
// text holds the user's private queue and chat, and what an agent prints lands
// in its transcript, so stdout gets it only with --text-stdout.
func runAppShot(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("app shot", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Bool("text", false, "no effect, kept for old callers: the text always goes to the file")
	withText := fs.Bool("text-stdout", false, "also print the window's visible text (private: read the file for the lines you need instead)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	out, err := appShot(appCommanderHome(), *withText)
	if err != nil {
		fmt.Fprintf(stderr, "cmdr app shot: %v\n", err)
		return 1
	}
	fmt.Fprint(stdout, out)
	return 0
}

func appShot(home string, withText bool) (string, error) {
	rawURL, err := os.ReadFile(filepath.Join(home, "api-url"))
	if err != nil {
		return "", fmt.Errorf("cmdrd is not running (no %s)", filepath.Join(home, "api-url"))
	}
	token, err := os.ReadFile(filepath.Join(home, "api-token"))
	if err != nil {
		return "", fmt.Errorf("no API token: %w", err)
	}
	req, _ := http.NewRequest(http.MethodPost, strings.TrimSpace(string(rawURL))+"/api/app/screenshot", nil)
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	resp, err := (&http.Client{Timeout: appShotWait}).Do(req)
	if err != nil {
		return "", fmt.Errorf("cmdrd does not answer: %w", err)
	}
	defer resp.Body.Close()
	var res struct {
		OK       bool   `json:"ok"`
		PNG      string `json:"png"`
		TextPath string `json:"text_path"`
		Text     string `json:"text"`
		Source   string `json:"source"`
		Warning  string `json:"warning"`
		Error    string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		if resp.StatusCode == http.StatusNotFound {
			return "", fmt.Errorf("this cmdrd has no screenshot endpoint yet (it needs a restart on a newer build)")
		}
		return "", fmt.Errorf("bad reply from cmdrd (HTTP %d)", resp.StatusCode)
	}
	if !res.OK {
		return "", fmt.Errorf("%s", res.Error)
	}
	var b strings.Builder
	if res.PNG != "" {
		fmt.Fprintf(&b, "png: %s\n", res.PNG)
	}
	if res.TextPath != "" {
		fmt.Fprintf(&b, "text: %s\n", res.TextPath)
	}
	fmt.Fprintf(&b, "source: %s\n", res.Source)
	if res.Warning != "" {
		fmt.Fprintf(&b, "warning: %s\n", res.Warning)
	}
	if withText {
		fmt.Fprintf(&b, "\n%s\n", res.Text)
	}
	return b.String(), nil
}
