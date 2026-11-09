package livedata

import (
	"os/exec"
	"regexp"
	"strings"
	"sync"
)

var (
	repoMu    sync.Mutex
	repoCache = map[string]string{} // project path -> owner/name ("" = none); a remote rarely changes
	githubURL = regexp.MustCompile(`github\.com[:/]([^/\s]+/[^/\s]+?)(?:\.git)?/?$`)
)

// GitHubRepo is the owner/name of the GitHub origin of the git checkout at
// path, or "" when there is none. Cached per path: the snapshot is rebuilt
// every few seconds and the remote does not change.
func GitHubRepo(path string) string {
	repoMu.Lock()
	defer repoMu.Unlock()
	if r, ok := repoCache[path]; ok {
		return r
	}
	out, err := exec.Command("git", "-C", path, "remote", "get-url", "origin").Output()
	r := ""
	if err == nil {
		r = ParseGitHubRepo(strings.TrimSpace(string(out)))
	}
	repoCache[path] = r
	return r
}

// ParseGitHubRepo pulls owner/name out of a GitHub remote URL (https or ssh).
func ParseGitHubRepo(url string) string {
	if m := githubURL.FindStringSubmatch(url); m != nil {
		return m[1]
	}
	return ""
}
