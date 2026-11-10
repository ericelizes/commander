package ship

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// fakeSecret is built at run time so this file holds no literal a scan of the
// repo itself would flag.
var fakeSecret = "AKIA" + "QYLPMN5HHHFPZAM2"

// mergeSecretRepo is newRepo whose branch feat ends in a merge of a side
// branch that adds a secret no parent has (only the merge commit carries it).
func mergeSecretRepo(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)
	git(t, dir, "checkout", "-q", "-b", "side", "main")
	commit(t, dir, "c")
	git(t, dir, "checkout", "-q", "feat")
	git(t, dir, "merge", "-q", "--no-ff", "--no-commit", "side")
	if err := os.WriteFile(filepath.Join(dir, "cfg.txt"), []byte("aws_key = \""+fakeSecret+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "cfg.txt")
	git(t, dir, "commit", "-qm", "merge side")
	git(t, dir, "checkout", "-q", "main")
	return dir
}

// gitLogLikeGitleaks runs what gitleaks runs for --log-opts: `git -C <source>
// log -p -U0 <opts split on spaces>`.
func gitLogLikeGitleaks(t *testing.T, args []string) string {
	t.Helper()
	var source, opts string
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--source":
			source = args[i+1]
		case "--log-opts":
			opts = args[i+1]
		}
	}
	argv := append([]string{"-C", source, "log", "-p", "-U0"}, strings.Split(opts, " ")...)
	out, err := exec.Command("git", argv...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", argv, err, out)
	}
	return string(out)
}

// A secret only a merge commit adds is in what gitleaks reads (plain `git log
// -p` prints no diff for a merge), so the ship refuses.
func TestShipScanCoversMergeCommits(t *testing.T) {
	dir := mergeSecretRepo(t)
	if strings.Contains(git(t, dir, "log", "-p", "-U0", "main..feat"), fakeSecret) {
		t.Fatal("setup: plain git log -p already shows the merge's secret")
	}
	gl := func(string) (string, error) { return "/bin/gitleaks", nil }
	run := func(_ context.Context, _ string, name string, args ...string) (string, error) {
		if name == "gitleaks" && strings.Contains(gitLogLikeGitleaks(t, args), fakeSecret) {
			return "Finding: aws-access-token\n" + gitleaksLeak, os.ErrInvalid
		}
		if name == "gitleaks" {
			return gitleaksClean, nil
		}
		return "", nil
	}
	err := Run(context.Background(), Options{Root: dir, Branch: "feat", LookPath: gl, Run: run}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "secret findings") {
		t.Fatalf("want a refusal for the merge's secret, got %v", err)
	}
}

// The same with the real gitleaks, when one is on PATH (or named by
// COMMANDER_TEST_GITLEAKS).
func TestShipRealGitleaksCatchesAMergeSecret(t *testing.T) {
	bin := realGitleaks(t)
	dir := mergeSecretRepo(t)
	gl := func(string) (string, error) { return bin, nil }
	// Only the scan is real; the build and test checks are not this test's.
	checks := func(ctx context.Context, rdir, name string, args ...string) (string, error) {
		if name != "gitleaks" {
			return "", nil
		}
		return runCommand(ctx, rdir, bin, args...)
	}
	err := Run(context.Background(), Options{Root: dir, Branch: "feat", LookPath: gl, Run: checks}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "secret findings") {
		t.Fatalf("want a refusal for the merge's secret, got %v", err)
	}
}

func realGitleaks(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("COMMANDER_TEST_GITLEAKS"); p != "" {
		return p
	}
	p, err := exec.LookPath("gitleaks")
	if err != nil {
		t.Skip("no gitleaks on PATH (set COMMANDER_TEST_GITLEAKS to run)")
	}
	return p
}

// workflowScanScript is the run block of the gitleaks workflow's scan step.
func workflowScanScript(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "gitleaks.yml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)- name: Scan with trusted rules\n.*?        run: \|\n(.*)`).FindStringSubmatch(string(body))
	if m == nil {
		t.Fatal("no scan step in gitleaks.yml")
	}
	var lines []string
	for _, l := range strings.Split(m[1], "\n") {
		if l != "" && !strings.HasPrefix(l, "          ") {
			break
		}
		lines = append(lines, strings.TrimPrefix(l, "          "))
	}
	return strings.Join(lines, "\n")
}

// pushToMain is a repo (the Action's checkout) whose main moved from before
// to the pushed commit; the push adds a secret and then a .gitleaksignore (and
// .gitleaks.toml) meant to silence it.
func pushToMain(t *testing.T, secretFingerprint func(dir, secretCommit string) string) (dir, before, pushed string) {
	t.Helper()
	dir = t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	addWorkflowPolicy(t, dir)
	os.WriteFile(filepath.Join(dir, ".gitleaksignore"), []byte("before-fingerprint\n"), 0o644)
	git(t, dir, "add", ".gitleaksignore")
	git(t, dir, "commit", "-qm", "main's allow list")
	before = git(t, dir, "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(dir, "cfg.txt"), []byte("aws_key = \""+fakeSecret+"\"\n"), 0o644)
	git(t, dir, "add", "cfg.txt")
	git(t, dir, "commit", "-qm", "secret")
	secret := git(t, dir, "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(dir, ".gitleaksignore"), []byte(secretFingerprint(dir, secret)+"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitleaks.toml"), []byte("title = \"pushed\"\n[allowlist]\npaths = ['''.*''']\n"), 0o644)
	git(t, dir, "add", ".gitleaksignore", ".gitleaks.toml")
	git(t, dir, "commit", "-qm", "allow it")
	pushed = git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "update-ref", "refs/remotes/origin/main", pushed) // the Action's origin/main is the pushed commit
	return dir, before, pushed
}

// runWorkflowScan runs the workflow's scan step in dir with gitleaks = bin
// (found through a fake `go env GOPATH`).
func runWorkflowScan(t *testing.T, dir, bin string, env ...string) (string, error) {
	t.Helper()
	tmp := t.TempDir()
	gopath := filepath.Join(tmp, "gopath")
	os.MkdirAll(filepath.Join(gopath, "bin"), 0o755)
	if err := os.Symlink(bin, filepath.Join(gopath, "bin", "gitleaks")); err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(tmp, "tools")
	os.MkdirAll(tools, 0o755)
	os.WriteFile(filepath.Join(tools, "go"), []byte("#!/bin/sh\necho "+gopath+"\n"), 0o755)
	cmd := exec.Command("bash", "-eo", "pipefail", "-c", workflowScanScript(t))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"),
		"RUNNER_TEMP="+filepath.Join(tmp, "runner"), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Tool rules are fixed at the trusted tool revision, with no repository
// ignore policy. New main or a missing vendored policy fails closed.
func TestWorkflowUsesToolRules(t *testing.T) {
	dir, before, pushed := pushToMain(t, func(string, string) string { return "pushed-fingerprint" })
	fake := stubGitleaks(t)
	out, err := runWorkflowScan(t, dir, fake, "SCAN="+pushed, "EVENT=push", "REF=refs/heads/main", "BEFORE="+before)
	if err == nil || !strings.Contains(out, "leaks found") {
		t.Fatalf("want findings: %v\n%s", err, out)
	}
	out, err = runWorkflowScan(t, dir, fake, "SCAN="+pushed, "EVENT=push", "REF=refs/heads/main", "BEFORE="+strings.Repeat("0", 40))
	if err == nil || !strings.Contains(out, "no trusted tool revision") {
		t.Fatalf("new main must fail closed: %v\n%s", err, out)
	}
}

func addWorkflowPolicy(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "internal", "ship", "gitleaks.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(defaultGitleaksConfig), 0644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "internal/ship/gitleaks.toml")
}

// End to end with the real gitleaks: a push to main whose own .gitleaksignore
// names its secret's fingerprint is still red.
func TestWorkflowRealGitleaksPushedIgnoreDoesNotSilenceItself(t *testing.T) {
	bin := realGitleaks(t)
	dir, before, pushed := pushToMain(t, func(dir, secret string) string {
		return secret + ":cfg.txt:aws-access-token:1"
	})
	// Sanity: the pushed list really does silence the finding when trusted.
	src := filepath.Join(t.TempDir(), "src")
	git(t, dir, "worktree", "add", "-q", "--detach", src, pushed)
	if out, err := exec.Command(bin, "detect", "--source", src, "--log-opts", pushed, "--no-banner", "--exit-code", "1").CombinedOutput(); err != nil {
		t.Fatalf("setup: the pushed .gitleaksignore does not silence the secret: %v\n%s", err, out)
	}
	git(t, dir, "worktree", "remove", "--force", src)
	out, err := runWorkflowScan(t, dir, bin, "SCAN="+pushed, "EVENT=push", "REF=refs/heads/main", "BEFORE="+before)
	if err == nil || !strings.Contains(out, "leaks found") {
		t.Fatalf("want a red scan, got err=%v\n%s", err, out)
	}
}

// What gitleaks prints for a clean scan and for findings (its own log lines,
// colours left out).
const (
	gitleaksClean = "1:00AM INF 1 commits scanned.\n1:00AM INF scan completed in 1ms\n1:00AM INF no leaks found\n"
	gitleaksLeak  = "1:00AM INF 1 commits scanned.\n1:00AM INF scan completed in 1ms\n1:00AM WRN leaks found: 1\n"
)

// stubGitleaks writes a stand-in that behaves as gitleaks v8.21.2 does where
// these scans depend on it: it reads `git -C <source> log -p -U0 <log-opts>`,
// honours an inline gitleaks:allow unless --ignore-gitleaks-allow is given,
// and when git writes anything to stderr it logs ERR "failed to scan Git
// repository", then "scan completed" and "no leaks found", and exits 0.
// FAKE_GIT_STDERR=1 adds a stderr line as an ambiguous ref would.
func stubGitleaks(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "gitleaks")
	script := `#!/bin/sh
src=. opts= allow=gitleaks:allow
while [ $# -gt 0 ]; do
  case "$1" in
    --source) src=$2; shift;;
    --log-opts) opts=$2; shift;;
    --config|--gitleaks-ignore-path|--log-level|--exit-code|--report-path|--report-format) shift;;
    --ignore-gitleaks-allow) allow=no-inline-allow-is-honoured;;
  esac
  shift
done
tmp=$(mktemp -d)
git -C "$src" log -p -U0 $opts > "$tmp/out" 2> "$tmp/err"
[ "$FAKE_GIT_STDERR" = 1 ] && echo "warning: refname 'main' is ambiguous." >> "$tmp/err"
if [ -s "$tmp/err" ]; then
  sed 's/^/1:00AM ERR [git] /' "$tmp/err"
  echo '1:00AM ERR failed to scan Git repository error="stderr is not empty"'
  echo '1:00AM INF scan completed in 1ms'
  echo '1:00AM INF no leaks found'
  rm -rf "$tmp"; exit 0
fi
echo '1:00AM INF scan completed in 1ms'
if grep -a '^+' "$tmp/out" | grep -av "$allow" | grep -aq AKIA; then
  echo '1:00AM WRN leaks found: 1'; rm -rf "$tmp"; exit 1
fi
echo '1:00AM INF no leaks found'
rm -rf "$tmp"
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// branchAdding is newRepo whose branch feat also adds files (path -> body).
func branchAdding(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := newRepo(t)
	git(t, dir, "checkout", "-q", "feat")
	for p, body := range files {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, dir, "add", p)
	}
	git(t, dir, "commit", "-qm", "add files")
	git(t, dir, "checkout", "-q", "main")
	return dir
}

// shipWithScanner ships feat with gitleaks = bin and no other check.
func shipWithScanner(dir, bin string) error {
	gl := func(string) (string, error) { return bin, nil }
	run := func(ctx context.Context, rdir, name string, args ...string) (string, error) {
		if name != "gitleaks" {
			return "", nil
		}
		return runCommand(ctx, rdir, bin, args...)
	}
	return Run(context.Background(), Options{Root: dir, Branch: "feat", LookPath: gl, Run: run, DryRun: true}, &bytes.Buffer{})
}

// The ways a branch could make the scan pass without scanning its secret.
// Each case is red with the stand-in, as with the real gitleaks below.
var bypasses = []struct {
	name  string
	files map[string]string
	setup func(t *testing.T, dir string)
}{
	{"inline gitleaks:allow", map[string]string{"cfg.txt": "aws_key = \"" + fakeSecret + "\" # gitleaks:allow\n"}, nil},
	{"a -diff attribute", map[string]string{"cfg.txt": "aws_key = \"" + fakeSecret + "\"\n", ".gitattributes": "* -diff\n"}, nil},
	{"a NUL byte", map[string]string{"cfg.txt": "\x00\naws_key = \"" + fakeSecret + "\"\n"}, nil},
	{"a tag named main", map[string]string{"cfg.txt": "aws_key = \"" + fakeSecret + "\"\n"}, func(t *testing.T, dir string) {
		git(t, dir, "tag", "main", "feat") // `main..feat` would be ambiguous (and empty read as the tag)
	}},
}

// The scan itself is judged (scanSecrets): a ship already refuses to start
// while a tag shares main's name ("is on heads/main"), but one can be made
// while the ship waits in line.
func scanBypasses(t *testing.T, bin string) {
	for _, c := range bypasses {
		t.Run(c.name, func(t *testing.T) {
			dir := branchAdding(t, c.files)
			if c.setup != nil {
				c.setup(t, dir)
			}
			opts := Options{Root: dir, LookPath: func(string) (string, error) { return bin, nil }}
			run := func(ctx context.Context, rdir, _ string, args ...string) (string, error) {
				return runCommand(ctx, rdir, bin, args...)
			}
			feat := git(t, dir, "rev-parse", "refs/heads/feat")
			if err := scanSecrets(context.Background(), opts, run, "main", feat, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "secret findings") {
				t.Fatalf("want a refusal for the secret, got %v", err)
			}
		})
	}
}

func TestShipScanBypassesAreRed(t *testing.T) {
	scanBypasses(t, stubGitleaks(t))
	t.Run("whole ship", func(t *testing.T) {
		dir := branchAdding(t, bypasses[0].files)
		if err := shipWithScanner(dir, stubGitleaks(t)); err == nil || !strings.Contains(err.Error(), "secret findings") {
			t.Fatalf("want a refusal for the secret, got %v", err)
		}
	})
}

func TestShipRealGitleaksBypassesAreRed(t *testing.T) {
	scanBypasses(t, realGitleaks(t))
}

// gitleaks exits 0 with "no leaks found" after a scan that failed (git wrote
// to stderr): the ship refuses, as for any run that did not say it completed
// or that exited non-zero with no findings.
func TestShipRefusesAScanThatDidNotComplete(t *testing.T) {
	t.Run("git wrote to stderr", func(t *testing.T) {
		t.Setenv("FAKE_GIT_STDERR", "1")
		err := shipWithScanner(newRepo(t), stubGitleaks(t))
		if err == nil || !strings.Contains(err.Error(), "did not complete") || !strings.Contains(err.Error(), "failed to scan") {
			t.Fatalf("want a refusal for the failed scan, got %v", err)
		}
	})
	for _, c := range []struct {
		name, out string
		exit      error
		want      string
	}{
		{"coloured ERR line", "\x1b[90m1:00AM\x1b[0m \x1b[1m\x1b[31mERR\x1b[0m\x1b[0m failed to scan Git repository\n" + gitleaksClean, nil, "did not complete"},
		{"partial scan", "1:00AM WRN partial scan completed in 1ms\n1:00AM WRN no leaks found in partial scan\n", nil, "did not complete"},
		{"no completion line", "", nil, `no "scan completed" line`},
		{"fatal error", "1:00AM FTL could not load config\n", os.ErrInvalid, "did not complete"},
		{"non-zero exit, no findings", gitleaksClean, os.ErrInvalid, "scan of"},
	} {
		t.Run(c.name, func(t *testing.T) {
			gl := func(string) (string, error) { return "/bin/gitleaks", nil }
			run := func(_ context.Context, _ string, name string, _ ...string) (string, error) {
				if name == "gitleaks" {
					return c.out, c.exit
				}
				t.Fatalf("ran %s after an incomplete scan", name)
				return "", nil
			}
			err := Run(context.Background(), Options{Root: newRepo(t), Branch: "feat", LookPath: gl, Run: run}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "secret findings") {
				t.Fatalf("want %q, got %v", c.want, err)
			}
		})
	}
}

// The scanned content cannot pass for gitleaks' own log: only lines shaped
// like its log lines count.
func TestReadGitleaksLog(t *testing.T) {
	leaks, why := readGitleaksLog("Secret: 1:00AM INF scan completed\n")
	if leaks || why == "" {
		t.Fatalf("content read as a log line: leaks=%v why=%q", leaks, why)
	}
	if leaks, why := readGitleaksLog(gitleaksLeak); !leaks || why != "" {
		t.Fatalf("findings: leaks=%v why=%q", leaks, why)
	}
	if leaks, why := readGitleaksLog(gitleaksClean); leaks || why != "" {
		t.Fatalf("clean: leaks=%v why=%q", leaks, why)
	}
}

// The workflow's scan step is red for each bypass and for a scan that did not
// complete, with the stand-in (and the real gitleaks when there is one).
func TestWorkflowScanBypassesAreRed(t *testing.T) {
	bins := map[string]string{"stand-in": stubGitleaks(t)}
	if p := os.Getenv("COMMANDER_TEST_GITLEAKS"); p != "" {
		bins["real"] = p
	} else if p, err := exec.LookPath("gitleaks"); err == nil {
		bins["real"] = p
	}
	for kind, bin := range bins {
		for _, c := range bypasses {
			t.Run(kind+"/"+c.name, func(t *testing.T) {
				dir := branchAdding(t, c.files)
				if c.setup != nil {
					c.setup(t, dir)
				}
				git(t, dir, "update-ref", "refs/remotes/origin/main", "refs/heads/main")
				out, err := runWorkflowScan(t, dir, bin, "SCAN="+git(t, dir, "rev-parse", "refs/heads/feat"), "EVENT=push", "REF=refs/heads/feat", "BEFORE=")
				if err == nil || !strings.Contains(out, "leaks found") {
					t.Fatalf("want a red scan, got err=%v\n%s", err, out)
				}
			})
		}
	}
	t.Run("stand-in/git wrote to stderr", func(t *testing.T) {
		dir := newRepo(t)
		git(t, dir, "update-ref", "refs/remotes/origin/main", "refs/heads/main")
		out, err := runWorkflowScan(t, dir, bins["stand-in"], "SCAN="+git(t, dir, "rev-parse", "feat"), "EVENT=push", "REF=refs/heads/feat", "BEFORE=", "FAKE_GIT_STDERR=1")
		if err == nil || !strings.Contains(out, "did not complete") {
			t.Fatalf("want a red scan, got err=%v\n%s", err, out)
		}
	})
	t.Run("stand-in/clean", func(t *testing.T) {
		dir := newRepo(t)
		git(t, dir, "update-ref", "refs/remotes/origin/main", "refs/heads/main")
		if out, err := runWorkflowScan(t, dir, bins["stand-in"], "SCAN="+git(t, dir, "rev-parse", "feat"), "EVENT=push", "REF=refs/heads/feat", "BEFORE="); err != nil {
			t.Fatalf("clean branch is red: %v\n%s", err, out)
		}
	})
}

// A pushed tag named origin/main never supplies the trusted rules.
func TestWorkflowTrustsTheRemoteBranchNotATag(t *testing.T) {
	if !strings.Contains(workflowScanScript(t), "trusted=refs/remotes/origin/main\n") {
		t.Fatal("the trusted rules are read through a short ref name")
	}
}

// Exercise the actual pinned detector, not an imitation of its path rules or
// multiline parser. Every fixture is invented and has no usable credential.
func TestRealGitleaksToolPolicyAndCumulativeDiff(t *testing.T) {
	bin := realGitleaks(t)
	for _, path := range []string{"go.sum", "package-lock.json", "notes.svg", "config.v2", "nested/gitleaks.toml.go", "node_modules/a.js", "@octokit/auth-token/README.md"} {
		t.Run(path, func(t *testing.T) {
			dir := newRepo(t)
			git(t, dir, "checkout", "feat")
			full := filepath.Join(dir, path)
			if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
				t.Fatal(err)
			}
			// GitHub PAT also exercises the removed per-rule path allowlist.
			token := "ghp_" + "aB3dE5fG7hJ9kL2mN4pQ6rS8tU1vW3xY5zA7"
			if err := os.WriteFile(full, []byte("aws_key = \""+fakeSecret+"\"\ngithub_token = \""+token+"\"\n"), 0644); err != nil {
				t.Fatal(err)
			}
			git(t, dir, "add", path)
			git(t, dir, "commit", "-qm", "invented credentials")
			git(t, dir, "checkout", "main")
			checkBothScansRed(t, dir, bin, "leaks found")
		})
	}
	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprintf("split private key/deleted=%v", deleted), func(t *testing.T) {
			dir := newRepo(t)
			git(t, dir, "checkout", "feat")
			body := "-----BEGIN RSA PRIVATE" + " KEY-----\ninvented-test-material\n"
			os.WriteFile(filepath.Join(dir, "key.pem"), []byte(body), 0644)
			git(t, dir, "add", "key.pem")
			git(t, dir, "commit", "-qm", "key start")
			os.WriteFile(filepath.Join(dir, "key.pem"), []byte(body+"-----END RSA PRIVATE"+" KEY-----\n"), 0644)
			git(t, dir, "add", "key.pem")
			git(t, dir, "commit", "-qm", "key end")
			// Positive control: the assembled intermediate tree must be detected.
			git(t, dir, "checkout", "main")
			checkBothScansRed(t, dir, bin, "leaks found")
			if deleted {
				git(t, dir, "checkout", "feat")
				git(t, dir, "rm", "key.pem")
				git(t, dir, "commit", "-qm", "delete assembled key")
				git(t, dir, "checkout", "main")
			}
			checkBothScansRed(t, dir, bin, "leaks found")
		})
	}
	t.Run("secret deleted from final tree", func(t *testing.T) {
		dir := branchAdding(t, map[string]string{"cfg.txt": "aws_key = \"" + fakeSecret + "\"\n"})
		git(t, dir, "checkout", "feat")
		git(t, dir, "rm", "cfg.txt")
		git(t, dir, "commit", "-qm", "remove credential")
		git(t, dir, "checkout", "main")
		checkBothScansRed(t, dir, bin, "leaks found")
	})
}

func checkBothScansRed(t *testing.T, dir, bin, want string) {
	t.Helper()
	tip := git(t, dir, "rev-parse", "feat")
	base := git(t, dir, "rev-parse", "main")
	run := func(ctx context.Context, rdir, _ string, args ...string) (string, error) {
		return runCommand(ctx, rdir, bin, args...)
	}
	err := scanSecrets(context.Background(), Options{Root: dir, LookPath: func(string) (string, error) { return bin, nil }}, run, "main", tip, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "secret findings") {
		t.Fatalf("ship: %v", err)
	}
	git(t, dir, "update-ref", "refs/remotes/origin/main", base)
	for _, event := range []string{"push", "pull_request"} {
		out, err := runWorkflowScan(t, dir, bin, "SCAN="+tip, "EVENT="+event, "REF=refs/heads/feat", "BEFORE="+base)
		if err == nil || !strings.Contains(out, want) {
			t.Fatalf("workflow %s: %v\n%s", event, err, out)
		}
	}
}

// A detector upgrade must not accidentally restore default global/path skips.
func TestVendoredGitleaksPolicyHasNoGlobalOrPathAllows(t *testing.T) {
	var policy map[string]any
	if _, err := toml.Decode(defaultGitleaksConfig, &policy); err != nil {
		t.Fatal(err)
	}
	if _, ok := policy["allowlist"]; ok {
		t.Fatal("global allowlist restored")
	}
	if _, ok := policy["extend"]; ok {
		t.Fatal("default inheritance restored")
	}
	var visit func(any)
	visit = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if _, ok := v["paths"]; ok {
				t.Fatal("path allowlist restored")
			}
			for _, child := range v {
				visit(child)
			}
		case []map[string]any:
			for _, child := range v {
				visit(child)
			}
		case []any:
			for _, child := range v {
				visit(child)
			}
		}
	}
	visit(policy)
	if rules, ok := policy["rules"].([]map[string]any); !ok || len(rules) != 195 {
		t.Fatal("missing default rules")
	}
}
