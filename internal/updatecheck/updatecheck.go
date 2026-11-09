// Package updatecheck compares the installed Commander build with the main
// branch of the local checkout: how far behind it is, which commits it lacks,
// and for each commit whether a review is on record.
//
// It is pure reads: local git only (plus git ls-remote when an origin exists,
// never fatal) and the beads JSONL export; it never runs br, fetches, builds or
// installs anything.
//
// Review marker rule (honest, never invented): a commit is "reviewed" only
// when evidence exists, otherwise "not confirmed". Evidence is either
//   - a Reviewed-by: trailer on the commit, or
//   - a comment in the commander project's beads (issues.jsonl) that names the
//     commit hash (full, or a prefix of at least 7 characters) and carries a
//     verdict line that is exactly CLEAN (a review Operator's closing line).
//     A comment that also has a NOT CLEAN line is never evidence.
package updatecheck

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"github.com/example-user/commander/internal/binstage"
)

// MaxCommits caps the commit list.
const MaxCommits = 50

// Review markers.
const (
	Reviewed     = "reviewed"
	NotConfirmed = "not confirmed"
)

// Commit is one commit the installed build lacks.
type Commit struct {
	Hash     string `json:"hash"`
	Subject  string `json:"subject"`
	Review   string `json:"review"`             // Reviewed or NotConfirmed
	Evidence string `json:"evidence,omitempty"` // "Reviewed-by: x" or "bead <id>"
}

// Status is the answer of Check.
type Status struct {
	Running   string `json:"running"`   // this process's build stamp ("" when unknown)
	Installed string `json:"installed"` // VERSION in the install dir ("" when none)
	Main      string `json:"main"`      // tip of main in the checkout
	Remote    string `json:"remote,omitempty"`
	// RemoteAhead is true when origin's main is a commit the checkout has not
	// fetched; the check never fetches, so this is a hint only.
	RemoteAhead bool     `json:"remote_ahead,omitempty"`
	Behind      int      `json:"behind"`
	Commits     []Commit `json:"commits"`
	Truncated   bool     `json:"truncated,omitempty"`
	UpToDate    bool     `json:"up_to_date"`
	// Dirty is true when the checkout has uncommitted changes (an install would refuse).
	Dirty bool   `json:"dirty"`
	Error string `json:"error,omitempty"`
}

// GitFunc runs git in dir and returns stdout.
type GitFunc func(ctx context.Context, dir string, args ...string) (string, error)

// ExecGit is the real GitFunc.
func ExecGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return string(out), fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(ee.Stderr)))
		}
		return string(out), err
	}
	return string(out), nil
}

// Options configure Check.
type Options struct {
	Repo       string
	InstallDir string
	// Running is this process's stamp; "" reads the build info (RunningStamp).
	Running string
	// Branch is the tracked branch; "" = main.
	Branch string
	// BeadsDir holds issues.jsonl of the commander project (<project>/.beads); "" = no bead evidence.
	BeadsDir string
	Git      GitFunc // nil = ExecGit
}

// RunningStamp is the vcs.revision the running binary was built from, with
// "-dirty" when the tree was modified; "" when the build carries none.
func RunningStamp() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	var rev string
	var dirty bool
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev != "" && dirty {
		rev += "-dirty"
	}
	return rev
}

// Commit hash of a stamp: the VERSION file's "<rev>[-dirty]".
func stampHash(stamp string) string { return strings.TrimSuffix(strings.TrimSpace(stamp), "-dirty") }

// Check compares the installed build with main. Failures land in Status.Error;
// the status is always returned.
func Check(ctx context.Context, o Options) Status {
	git := o.Git
	if git == nil {
		git = ExecGit
	}
	branch := o.Branch
	if branch == "" {
		branch = "main"
	}
	st := Status{Running: o.Running, Installed: binstage.Revision(o.InstallDir), Commits: []Commit{}}
	if st.Running == "" {
		st.Running = RunningStamp()
	}
	if o.Repo == "" {
		st.Error = "the commander checkout is not known (set COMMANDER_REPO)"
		return st
	}
	out, err := git(ctx, o.Repo, "rev-parse", "--verify", "refs/heads/"+branch)
	if err != nil {
		st.Error = fmt.Sprintf("cannot read %s in %s: %v", branch, o.Repo, err)
		return st
	}
	st.Main = strings.TrimSpace(out)
	if porcelain, err := git(ctx, o.Repo, "status", "--porcelain", "--untracked-files=no"); err == nil {
		st.Dirty = strings.TrimSpace(porcelain) != ""
	}
	st.Remote, st.RemoteAhead = remoteMain(ctx, git, o.Repo, branch)

	base := stampHash(st.Installed)
	if base == "" {
		base = stampHash(st.Running)
	}
	if base == "" || base == "unknown" {
		st.Error = "no installed build stamp to compare with"
		return st
	}
	if _, err := git(ctx, o.Repo, "cat-file", "-e", base+"^{commit}"); err != nil {
		st.Error = fmt.Sprintf("installed revision %s is not in %s", short(base), o.Repo)
		return st
	}
	cnt, err := git(ctx, o.Repo, "rev-list", "--count", base+".."+st.Main)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	fmt.Sscanf(strings.TrimSpace(cnt), "%d", &st.Behind)
	st.UpToDate = st.Behind == 0
	if st.Behind == 0 {
		return st
	}
	log, err := git(ctx, o.Repo, "log", fmt.Sprintf("--max-count=%d", MaxCommits),
		"--format=%H%x1f%s%x1f%(trailers:key=Reviewed-by,valueonly,separator=%x2c )%x1e", base+".."+st.Main)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	beadEvidence := map[string]string{}
	if o.BeadsDir != "" {
		beadEvidence = beadReviews(o.BeadsDir)
	}
	for _, rec := range strings.Split(log, "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		f := strings.Split(rec, "\x1f")
		if len(f) < 3 || f[0] == "" {
			continue
		}
		c := Commit{Hash: f[0], Subject: f[1], Review: NotConfirmed}
		if by := strings.TrimSpace(f[2]); by != "" {
			c.Review, c.Evidence = Reviewed, "Reviewed-by: "+by
		} else if id := lookup(beadEvidence, c.Hash); id != "" {
			c.Review, c.Evidence = Reviewed, "bead "+id
		}
		st.Commits = append(st.Commits, c)
	}
	st.Truncated = st.Behind > len(st.Commits)
	return st
}

func short(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// remoteMain asks origin for its branch tip; "" with no remote or on any error.
func remoteMain(ctx context.Context, git GitFunc, repo, branch string) (string, bool) {
	remotes, err := git(ctx, repo, "remote")
	if err != nil || !containsLine(remotes, "origin") {
		return "", false
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := git(ctx, repo, "ls-remote", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", false
	}
	f := strings.Fields(out)
	if len(f) == 0 {
		return "", false
	}
	_, err = git(ctx, repo, "cat-file", "-e", f[0]+"^{commit}")
	return f[0], err != nil
}

func containsLine(s, want string) bool {
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) == want {
			return true
		}
	}
	return false
}

var hashRe = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
var cleanRe = regexp.MustCompile(`(?m)^\W*(?:verdict:\s*)?CLEAN\W*$`)
var notCleanRe = regexp.MustCompile(`(?mi)\bNOT CLEAN\b`)

// beadReviews maps each hash named by a CLEAN review comment to its bead id.
// The keys are the hashes as written (7-40 chars).
func beadReviews(beadsDir string) map[string]string {
	f, err := os.Open(filepath.Join(beadsDir, "issues.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if !strings.Contains(string(line), "CLEAN") {
			continue
		}
		var b struct {
			ID       string `json:"id"`
			Comments []struct {
				Text string `json:"text"`
			} `json:"comments"`
		}
		if json.Unmarshal(line, &b) != nil {
			continue
		}
		for _, c := range b.Comments {
			if notCleanRe.MatchString(c.Text) || !cleanRe.MatchString(c.Text) {
				continue
			}
			for _, h := range hashRe.FindAllString(c.Text, -1) {
				if _, seen := out[h]; !seen {
					out[h] = b.ID
				}
			}
		}
	}
	return out
}

// lookup finds a bead whose comment names hash as a full hash or a prefix of >= 7 chars.
func lookup(evidence map[string]string, hash string) string {
	for h, id := range evidence {
		if strings.HasPrefix(hash, h) {
			return id
		}
	}
	return ""
}
