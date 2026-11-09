package managerguard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	t.Setenv("COMMANDER_AGENT_WRITE_GUARD", "off") // direct bd/br writes: agentwrite_test.go
	deny := []string{
		"git commit -m x", "git add . && git commit -m x", "git merge main", "git rebase main", "git push origin main",
		"git cherry-pick abc", "git reset --hard", "git checkout -b feat", "git stash", "git stash pop", "git am x.patch",
		"git apply x.patch", "git -C /repo/.worktrees/op-x commit -m x", "git -c a=b commit", "git branch newbranch", "git branch -D x", "git branch -m old new", "git branch --list x; git branch newbranch",
		"git worktree add foo", "git status; git commit -m x", "git status | cat && git push", "FOO=1 git commit",
		"pytest", "pytest -k x", "go test ./...", "cargo test", "npm test", "npm run test", "npm run test:unit", "uv run pytest",
		"make test", "vitest run", "jest", "npx vitest", "cd /repo/.worktrees/op-x && git commit -m x", "cd .worktrees/op-x; touch f",
		"sed -i s/a/b/ f", "sed -i'' s/a/b/ f", "sed -ni p f", "cat x | tee out.txt", "echo hi > file.txt", "echo hi >> internal/x.go",
		"echo hi &> out", "python3 -c \"open('f','w').write('x')\"", "python -c 'import shutil; shutil.rmtree(\"d\")'",
		"bash -c 'git commit -m x'", "sh -c \"cd .worktrees/a && ls\"", "echo $(git commit -m x)", "echo `git push`",
		"gh pr merge 3", "gh pr create", "gh workflow run ci.yml", "gh workflow enable x", "gh run rerun 123456789", "gh run cancel 5",
		"gh pr edit 23 --base feat/sample-worker-plan 2>&1 | tail -1", "gh pr close 3", "gh api --method POST repos/a/b/issues", "gh api -X POST repos/a/b/issues", "gh api repos/a/b -f x=1", "gh issue create",
		"gh api graphql -f query='mutation { addComment(input:{}) { clientMutationId } }'", "gh api graphql -F query=@q.graphql",
		"gh api graphql --input q.json",
		"curl -X POST http://x", "curl -d a=b http://x", "curl -o out.bin http://x", "rm -rf internal", "cp a b", "mv a b",
		"find . -delete", "sudo git commit", "env FOO=1 git push", "timeout 5 go test ./...", "perl -pi -e s/a/b/ f",
		"cat <<EOF\nhi\nEOF\ngit commit -m x", "git gc", "git config user.name x",
		"git tag v1", "git tag --list -d v1", "git tag -a v1 -m x",
		"cat > /workspace/example/code/p/.worktrees/x/f.md <<EOF\nhi\nEOF", "cat > /workspace/example/code/p/file <<'EOF'\nhi\nEOF",
		"cat > /tmp/../etc/x <<EOF\nhi\nEOF", "tee /tmp/../x", "tee ../x", "echo hi | tee /workspace/example/code/p/f",
		"python3 -c \"open('/tmp/../a','w').write('x')\"",
		"sed -i '' s/a/b/ /workspace/example/code/p/file.go", "sed -i s/a/b/ /tmp/ok /workspace/example/code/p/f", "perl -pi -e s/a/b/ /workspace/example/code/p/.worktrees/x/f",
		"sed -i '' s/a/b/ /tmp/../etc/hosts", "sed -i.bak s/a/b/ ../x", "sed -i s/a/b/", "sed -e s/a/b/ --in-place file.txt", "perl -i -pe s/a/b/ /tmp/ok ~/f",
	}
	allow := []string{
		"git status", "git ls-remote origin feat/x", "cmdr internal-inbox --agent manager-x 2>&1 | python3 -c \"import json,sys; print(json.load(sys.stdin)[0]['subject'])\"", "git log --oneline -5", "git diff main...HEAD", "git show HEAD", "git branch --list", "git branch -a",
		"git branch --list 'op/*'", "git branch -vv --list 'op/*'", "git branch -r", "git branch -v", "git branch --show-current", "git branch --merged main",
		"cmdr internal-inbox --agent X | python3 -c 'import json,sys; d=json.load(sys.stdin); sys.stdout.write(d[0][\"subject\"])'",
		"cmdr internal-inbox --agent X | python3 -c 'import json,sys; print(json.load(sys.stdin)[0][\"subject\"])'; git log --oneline -8; git branch --list 'op/*'",
		"git branch", "git fetch origin", "git worktree list", "git rev-parse HEAD", "git -C /repo status",
		"gh pr view 3", "gh pr list --state open", "gh pr checks 3", "gh pr diff 3", "gh run list", "gh run view 5",
		"gh api repos/a/b/pulls/3", "gh api -X GET repos/a/b",
		"gh api graphql -f query='query($n:Int!){repository(owner:\"a\",name:\"b\"){pullRequest(number:$n){reviewThreads(first:100){nodes{isResolved}}}}}' -F n=3 -q '.data'",
		"gh api graphql -f query='{viewer{login}}'", "gh workflow list", "gh workflow view ci.yml", "gh run watch 5", "gh auth status",
		"gh workflow list 2>&1 | head -15; cmdr internal-gate list --project example-app --bead example-app-t01.20 2>&1",
		"gh search prs foo", "gh release view v1", "gh repo list", "gh issue status", "br show x", "br -C /repo comments add x hi",
		"cmdr internal-spawn-operator --project p --step s --title t --prompt p", "cmdr internal-spawn-operator --project p --step s --title t --prompt p --account work --runtime codex", "cmdr internal-operator-provider operator-op-x --account work", "cmdr internal-operator-provider operator-op-x --runtime codex", "cmdr account list", "cmdr account use manager-x work", "cmdr runtime manager-x codex", "cmdr internal-gate check x", "cmdr internal-gate link --project p --bead g --before x", "cmdr internal-ship op/foo --project p", "cmdr tab ls", "cmdr app shot", "cmdr graph check --project p --json", "cmdr board --role manager-x", "cmdr app shot --text",
		"ls -la", "cat README.md", "grep -rn foo internal | head", "rg foo", "find . -name '*.go'", "jq . x.json",
		"curl -s http://127.0.0.1:8787/api/snapshot", "curl -X GET http://x", "echo hi > /dev/null", "ls 2>&1", "ls 2>/dev/null",
		"echo hi > /tmp/note.txt", "cmdr internal-mail --to commander --subject 'git commit later' --body 'go test it'",
		"echo 'git commit' | cat", "cd /repo && ls", "git status && br list", "cat <<EOF\ngit commit -m x\ngo test\nEOF",
		"cmdr internal-mail --body \"$(cat /tmp/body.txt)\" --to commander --subject s", "sleep 5", "date", "tmux ls",
		"python3 script.py", "python3 -c 'print(1)'", "rm /tmp/x", "tee /dev/null", "git branch --show-current", "git tag -l",
		"br dep rm a b; sed -i '' 's/x/y/' /tmp/t09-wf2.md; br update a --description \"$(cat /tmp/t09-wf2.md)\"; br show a",
		"sed -i s/a/b/ /private/tmp/claude-501/s/scratchpad/n.md", "sed -i.bak -e s/a/b/ $TMPDIR/x", "sed -n p file.txt", "perl -pi -e 's/a/b/' /tmp/a /tmp/b",
		"br dep add a b", "br update a --title t", "br create t --parent a", "br close a", "br label add a x", "br comments add a hi", "br sync --flush-only",
		"git remote -v", "git tag --list 'prod*' --contains abc1234 2>&1 | head -3", "git tag -l 'v*'", "git tag --points-at HEAD",
		"cat > /private/tmp/claude-501/-Users-x-code-p/e7a8/scratchpad/wf.md <<'EOF'\nbody\nEOF\nbr update p-1 --description \"$(cat /private/tmp/claude-501/-Users-x-code-p/e7a8/scratchpad/wf.md)\"",
		"tee /tmp/a", "echo hi | tee -a /private/tmp/claude-501/s/scratchpad/n.md", "cat > $TMPDIR/x <<EOF\nx\nEOF", "git config --get user.name", "echo \"a; b && c\"", "go version", "go build ./...",
	}
	for _, c := range deny {
		if v := Classify(c, ""); !v.Deny {
			t.Errorf("want DENY for %q", c)
		}
	}
	for _, c := range allow {
		if v := Classify(c, ""); v.Deny {
			t.Errorf("want ALLOW for %q (denied: %s)", c, v.Why)
		}
	}
}

func TestClassify_CwdInsideWorktree(t *testing.T) {
	t.Setenv("COMMANDER_AGENT_WRITE_GUARD", "off")
	for cwd, want := range map[string]bool{
		"/repo/.worktrees/op-x":     true,
		"/repo/.worktrees/op-x/sub": true,
		"/repo":                     false,
		"":                          false,
		"/repo/worktrees/op-x":      false,
		"/repo/not.worktrees/op-x":  false,
	} {
		if got := Classify("br update a --title t", cwd).Deny; got != want {
			t.Errorf("cwd %q: deny = %v, want %v", cwd, got, want)
		}
	}
}

func TestHookOutput(t *testing.T) {
	raw := string(HookOutput())
	for k, v := range map[string]string{"hookEventName": "PreToolUse", "permissionDecision": "deny"} {
		if !strings.Contains(raw, `"`+k+`":"`+v+`"`) {
			t.Errorf("missing %s=%s in %s", k, v, raw)
		}
	}
	if !json.Valid([]byte(raw)) {
		t.Errorf("not JSON: %s", raw)
	}
	if !strings.Contains(raw, "Managers do not do the work. Hand this to an Operator: cmdr internal-spawn-operator --project <p>") {
		t.Errorf("reason missing: %s", raw)
	}
}

func TestRefusalLogAndMailDedupe(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	if err := LogRefusal(home, "manager-x", "git commit", "git commit", now); err != nil {
		t.Fatal(err)
	}
	if err := LogRefusal(home, "manager-x", "go test", "go test", now); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, "logs", "manager-refusals.jsonl"))
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"role":"manager-x"`) || !strings.Contains(lines[0], `"command":"git commit"`) {
		t.Fatalf("log = %q", raw)
	}
	if !ShouldMail(home, "manager-x", now) {
		t.Fatal("first refusal must mail")
	}
	if ShouldMail(home, "manager-x", now.Add(29*time.Minute)) {
		t.Fatal("second refusal within 30m must not mail")
	}
	if !ShouldMail(home, "manager-y", now.Add(time.Minute)) {
		t.Fatal("another role mails independently")
	}
	if !ShouldMail(home, "manager-x", now.Add(31*time.Minute)) {
		t.Fatal("after 30m it mails again")
	}
}

func TestClassify_RefusedGateSetupLoopsAllowed(t *testing.T) {
	for _, c := range []string{
		`cd /repo/sample-app
for x in "1 ci-green" "2 threads-resolved"; do set -- $x; cmdr internal-gate rm --project sample-app --bead sample-app-demo.$1 --name $2 2>&1 | tail -1; done
g(){ cmdr internal-gate add --project sample-app --bead "$1" --name "$2" --check "$3" --verifier auto 2>&1 | tail -1; }
g sample-app-demo.1 ci-green "gh pr checks 24 --repo acme/sample-app >/dev/null"
g sample-app-demo.2 lint-and-tests "cd .worktrees/op-check-sample/sample_worker && just lint && just test"
for b in 1 2; do cmdr internal-gate check --project sample-app --bead sample-app-demo.$b 2>&1 | head -1; done`,
		// data inside quotes never executes
		"cmdr internal-gate add --check \"cd .worktrees/x && just test\" --name a",
		"cmdr internal-gate add --check \"\\$(gh api -X POST x)\" --name a",
		"g(){ cmdr internal-gate add --check \"$1\"; }\ng c",
		"for b in 1 2; do cmdr internal-gate check --bead x.$b | tail -1 | cut -c1-90; done",
		"set -- $x; cmdr internal-gate rm --bead $1",
	} {
		if v := Classify(c, ""); v.Deny {
			t.Errorf("want ALLOW for %q (denied: %s)", c, v.Why)
		}
	}
}

func TestClassify_ControlFlowStillJudgesExecutedWords(t *testing.T) {
	for _, c := range []string{
		"for b in 1 2; do git commit -m x; done",
		"if true; then git push; fi",
		"g(){ git commit -m x; }; g",
		"while true; do go test ./...; done",
		"echo \"$(git push)\"",
	} {
		if v := Classify(c, ""); !v.Deny {
			t.Errorf("want DENY for %q", c)
		}
	}
}

func TestClassify_RefusesTheInstallEndpoints(t *testing.T) {
	for _, cmd := range []string{
		`curl -s -H "Authorization: Bearer $(cat ~/.commander/api-token)" http://127.0.0.1:8787/api/update`,
		"curl -X POST $U/api/update/ticket", "bash -c 'curl $U/API/UPDATE/install'", "echo $(curl $U/api/update)",
		`curl $U/api/up""date`, `curl $U/api/'up'date/install`, `curl $U/api/up\date`, `curl $U/ap\i/upd\ate`,
		`curl $U/api/up$'\x64'ate`, `curl $U/api/up$'\144'ate`, `curl "$U/api / update"`, `curl $U/api/UP`,
		`python3 -c "import urllib.request as u; u.urlopen('http://127.0.0.1:8787/ap'+'i/upd'+'ate/install')"`,
		`node -e "fetch('http://localhost:8787/'+['update'].join())"`, `wget -qO- $(cat ~/.commander/api-url)/x/update`,
	} {
		if v := Classify(cmd, ""); !v.Deny {
			t.Errorf("%q was allowed", cmd)
		}
	}
	if v := Classify("curl http://127.0.0.1:8787/api/snapshot", ""); v.Deny {
		t.Errorf("snapshot read refused: %s", v.Why)
	}
}

var tmuxKillCommands = []string{
	"tmux kill-server", "tmux -L commander-ops kill-server", "tmux -S /tmp/x kill-server", "env tmux -L a kill-server",
	"/opt/homebrew/bin/tmux kill-server", "tmux kill-session -a", "tmux -L commander-ops kill-session -a",
	"tmux kill-session -t op-other", "tmux kill-session -t=op-x", "tmux kill-session -at x",
	"killall tmux", "pkill tmux", "pkill -f tmux", "pkill -9 -f 'tmux -L commander-ops'", "kill $(pgrep tmux)",
	"kill -TERM `pgrep -f commander-ops`; tmux ls", "tmux display -p '#{pid}' | xargs kill",
	"bash -c 'tmux kill-server'", "echo $(tmux kill-server)", "true && tmux kill-server",
	`printf 'tmux\tkill-server\n' | sh`, `bash -c $'tmux\tkill-server'`, `python3 -c "import os;os.system('tmux ls\npkill tmux')"`,
}

var tmuxReadCommands = []string{
	"tmux ls", "tmux -L commander-ops ls", "tmux list-sessions", "tmux list-panes -a", "tmux capture-pane -p -t x",
	"tmux kill-session", "tmux display -p '#{session_name}'", "kill 1234", "pkill -f gopls", "killall node",
	"grep tmux notes.md", "tmux has-session -t x",
}

func TestManagerGuardTmuxRefusesKills(t *testing.T) {
	for _, c := range tmuxKillCommands {
		if v := Classify(c, ""); !v.Deny {
			t.Errorf("%q was allowed", c)
		}
	}
}

func TestManagerGuardTmuxAllowsReads(t *testing.T) {
	for _, c := range tmuxReadCommands {
		if TmuxKill(c) != "" {
			t.Errorf("%q refused by TmuxKill", c)
		}
	}
}

// Invented compound read commands exercise shell syntax without copied refusal logs.
func TestClassify_PureReadsPass(t *testing.T) {
	for _, c := range []string{
		"git -C .worktrees/op-check-sample-demo.2.3 status --short 2>&1 | head -20",
		"cd /repo/.worktrees/op-check-example-app-t11.2.3 2>/dev/null && { git branch --show-current; git status --short | head -8; git log --oneline -4 | cut -c1-100; }",
		"cd /repo/.worktrees && for d in $(command ls | grep -E \"demo\\.(2\\.[123])\"); do echo \"$d $(stat -f '%Sp' $d) $(git -C $d rev-parse --abbrev-ref HEAD 2>/dev/null)\"; done; git -C .. worktree list",
		"tmux ls 2>&1 | cut -d: -f1; pgrep -fl \"claude -p You are the Operator\" | cut -c1-60; git -C .worktrees/op-x status --short 2>&1",
		"br show x-1 2>&1 | grep -A8 Comments | cut -c1-400; s=$(tmux ls | grep \"demo\" | cut -d: -f1); [ -n \"$s\" ] && tmux capture-pane -p -t $s -S -60 | tail -12; git -C .worktrees/op-x log --oneline -3",
		"git log --oneline main..op/x | head; git cherry main op/x | head -3; git branch --contains abc1234 | head -3",
		"br list --status in_progress --json 2>/dev/null | jq -r '.[]|.id' | head -20; git check-ignore -v bin/cmdr .worktrees | head; git worktree list | wc -l; git branch --no-merged main | wc -l",
		"git stash list", "cd /repo/.worktrees/op-x && ls -la && cat go.mod | head -3", "find .worktrees -maxdepth 1 -name 'op-*' | wc -l",
		"cmdr internal-inbox --agent manager-x --limit 30 2>&1 | head", "cmdr graph check --json > /dev/null", "ps aux | grep cmdrd | grep -v grep",
	} {
		if v := Classify(c, ""); v.Deny {
			t.Errorf("want ALLOW for %q (denied: %s)", c, v.Why)
		}
		if v := Classify(c, "/repo/.worktrees/op-x"); v.Deny {
			t.Errorf("want ALLOW from a worktree for %q (denied: %s)", c, v.Why)
		}
	}
}

// A write anywhere in the line, or a compound with a non-read, keeps the old rules.
func TestClassify_ReadsMixedWithWritesStillRefused(t *testing.T) {
	for _, c := range []string{
		"git -C .worktrees/op-x status && git -C .worktrees/op-x commit -m x",
		"cd .worktrees/op-x && ls && touch f",
		"cd .worktrees/op-x && go test ./...",
		"git log | head > out.txt",
		"git -C .worktrees/op-x log; sed -i s/a/b/ f",
		"cd .worktrees/op-x; find . -delete",
		"cd .worktrees/op-x; find . -exec rm {} +",
		"ls $(git -C .worktrees/op-x commit -m x)",
		"cd .worktrees/op-x; br close a",
		"cd .worktrees/op-x; git fetch",
		"cd .worktrees/op-x; python3 -c \"open('f','w').write('x')\"",
		"cd .worktrees/op-x; python3 -c \"open('f',mode='a')\"",
		"cd .worktrees/op-x; git stash",
		"cd .worktrees/op-x; tmux kill-session -t x",
		"cd .worktrees/op-x; cmdr internal-spawn-operator --project p",
		"git -C .worktrees/op-x rebase main", "git -C .worktrees/op-x push", "git -C .worktrees/op-x merge x",
		"cd .worktrees/op-x; tee f", "ls > f.txt",
	} {
		if v := Classify(c, ""); !v.Deny {
			t.Errorf("want DENY for %q", c)
		}
	}
}

// Review findings of demo.66.7: each reviewer example must be refused from a
// worktree and from a cd into one (away from one these writes follow the usual
// rules: scratch is fine); the "everywhere" ones are refused from any cwd.
func TestClassify_ReviewFindingsDenied(t *testing.T) {
	for _, c := range []string{
		// 1: interpreters are not pure reads
		`python3 -c "import os; os.system('touch f')"`, `python3 -c "import subprocess; subprocess.run(['touch','f'])"`,
		`python3 -c "import os; os.symlink('a','b')"`, `python3 -c "from pathlib import Path; Path('f').touch()"`,
		`python3 -c "exec('x')"`, `python3 -c "getattr(os,'system')('touch f')"`,
		`node -e "require('fs').rmSync('f')"`, `node -e "require('child_process').execSync('touch f')"`,
		`node -e "require('fs').openSync('f','w')"`, `ruby -e "system('touch f')"`, `ruby -e "File.delete('f')"`,
		`ruby -e "IO.popen('touch f')"`, `ruby -e "%x(touch f)"`, `ruby -e "File.new('f','w')"`,
		// 2: popen / variable modes are not read-only opens
		`python3 -c "import os; os.popen('rm x')"`, `python3 -c "m='w'; open('f',m)"`, `python3 -c "import os; os.open('f',65)"`,
		// 4: write/exec options of git read verbs
		`git log --output=f`, `git diff --output f`, `git show --output=f`, `git stash show --output=f`, `git shortlog --output=f`,
		`git -c core.fsmonitor='touch f' status`, `git -c core.pager='touch f' log`, `git -ccore.pager=x log`,
		`git -c diff.external='touch f' diff --ext-diff`, `git diff --textconv`, `git grep -O'touch f' x`, `git grep -Otouch x`, `git grep --open-files-in-pager=cmd x`,
		`git ls-remote --upload-pack=cmd origin`, `git ls-remote --exec=cmd origin`, `git ls-remote 'ext::sh -c touch% f'`, `git cat-file --filters HEAD:f`,
		`git --exec-path=/x status`, `git --config-env=core.pager=X log`,
		`GIT_EXTERNAL_DIFF=cmd git diff`, `GIT_PAGER='touch f' git log`,
		// 5: tmux formats run shell commands
		`tmux display-message -p '#(touch f)'`, `tmux ls -F '#(rm x)'`, `tmux list-panes -F "#(touch f)"`,
		// 6: sort -o, sed w/e
		`sort -o f f`, `sort f -o f`, `sort --output=f f`, `sort -ro f f`, `sort --compress-program=cmd f`,
		`sed -n 'w f' g`, `sed 's/a/b/w f' g`, `sed -n 's/a/b/gw f' g`, `sed '1e touch f' g`, `sed 's/a/b/e' g`, `sed -e 'w f' g`, `sed --expression='w f' g`, `sed -f script g`,
	} {
		if v := Classify(c, "/repo/.worktrees/op-x"); !v.Deny {
			t.Errorf("want DENY from a worktree for %q", c)
		}
		if v := Classify("cd /repo/.worktrees/op-x && "+c, ""); !v.Deny {
			t.Errorf("want DENY after cd into a worktree for %q", c)
		}
	}
	for _, c := range []string{
		// 3: >& file is a redirect
		`echo x >& f`, `echo x >&f`, `cat f >&g`, `echo x >& f 2>&1`,
		`git reflog expire --all`, `git reflog delete HEAD@{1}`, `git symbolic-ref HEAD refs/heads/x`, `git symbolic-ref -d HEAD`,
		`git merge-tree --write-tree a b`,
		// 7: process substitution runs its command
		`cat <(touch f)`, `echo x | tee >(cat)`, `cat f > >(tee g)`, `diff <(ls) <(touch f)`,
		// 8: wrapper flags with values
		`timeout -s KILL 5 rm x`, `timeout --signal=KILL 5 rm x`, `timeout -k 2 5 rm x`, `env -S 'rm x'`, `env -u FOO rm x`, `env -S'rm x'`, `env --split-string='rm x'`,
		`nice -n 5 rm x`, `sudo -u root rm x`, `time -p rm x`,
		// 9: token reads
		`cat ~/.commander/api-token`, `grep . ~/.commander/api-token`, `jq . ~/.commander/api-token`, `head -1 ~/.commander/api-token`,
		`tail ~/.commander/api-token`, `less ~/.commander/api-token`, `cat ~/.commander/api-t*`, `cat ~/.commander/api-to?en`, `cat ~/.commander/*`,
		`cat < ~/.commander/api-token`, `cat "$HOME/.commander/api-""token"`, `echo $(cat ~/.commander/api-token)`, `bash -c 'cat ~/.commander/api-token'`,
		`python3 -c "print(open('/srv/x/.commander/api-token').read())"`,
	} {
		for _, cwd := range []string{"", "/repo/.worktrees/op-x"} {
			if v := Classify(c, cwd); !v.Deny {
				t.Errorf("want DENY for %q (cwd %q)", c, cwd)
			}
		}
		if v := Classify("cd /repo/.worktrees/op-x && "+c, ""); !v.Deny {
			t.Errorf("want DENY after cd into a worktree for %q", c)
		}
	}
}

// The wrapper fixes must not refuse the plain uses, and the reads that
// were refused before the pure-read rule stay allowed.
func TestClassify_ReviewFindingsKeepReads(t *testing.T) {
	for _, c := range []string{
		"timeout 5 ls", "timeout -s KILL 5 ls", "env FOO=1 ls", "env -u FOO ls", "env -S 'ls -la'", "nice -n 5 ls", "sudo -u root ls", "time -p ls",
		"git merge-base main HEAD", "git reflog", "git reflog show HEAD", "git reflog -5", "git symbolic-ref HEAD", "git symbolic-ref --short HEAD", "git symbolic-ref -q HEAD",
		"git log --oneline --stat", "git diff --no-ext-diff --no-textconv HEAD", "git diff --stat", "git show --stat HEAD", "git ls-remote origin", "git grep -n foo", "git -C .worktrees/op-x log -3",
		"tmux display-message -p '#{session_name}'", "tmux ls -F '#{session_name}'", "tmux capture-pane -p -t x", "tmux list-panes -a",
		"sort f", "sort -n -k2 f", "sort -u f", "sed -n 1,5p f", "sed 's/a/b/' f", "sed -n '/warn/p' f", "sed -e 's/a/e/' f", "sed '/e/d' f", "sed -E 's/(a|b)/c/g' f",
		"cat f 2>&1", "ls >&2", "echo x >&2", "ls 2>&-", "cat f >& /dev/null", "diff <(echo a) <(echo b)", "cat <(ls)",
		"cat ~/.commander/logs/x.jsonl", "ls ~/.commander", "cat ~/.commander/config.toml", "grep foo ~/.commander/logs/ops.log",
	} {
		for _, cwd := range []string{"", "/repo/.worktrees/op-x"} {
			if v := Classify(c, cwd); v.Deny {
				t.Errorf("want ALLOW for %q (cwd %q; denied: %s)", c, cwd, v.Why)
			}
		}
	}
	// Reading with an interpreter is allowed away from a worktree (the usual rules), not inside one.
	for _, c := range []string{
		"python3 -c 'import json; a=[json.loads(l) for l in open(\"/tmp/x.jsonl\")]; print(len(a))'",
		"br list --json | python3 -c 'import json,sys; print(json.load(sys.stdin))'",
		`python3 -c "open('f').read()"`, `python3 -c "open('f','r').read()"`,
	} {
		if v := Classify(c, ""); v.Deny {
			t.Errorf("want ALLOW for %q (denied: %s)", c, v.Why)
		}
		if v := Classify(c, "/repo/.worktrees/op-x"); !v.Deny {
			t.Errorf("want DENY from a worktree for %q", c)
		}
	}
}

func TestLinearCredentialProtection(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		"cat ~/.commander/linear.key", "head -c 50 " + filepath.Join(home, ".commander", "linear.key"),
		"tail $COMMANDER_HOME/linear.key", "less ${COMMANDER_HOME}/linear.key",
		"grep x /tmp/linear.key", "jq . linear.key", "cat ~/.commander/linear*",
		"sh -c 'cat /tmp/linear.key'", "echo linear.key",
	} {
		if !Classify(command, "").Deny {
			t.Errorf("allowed %s", command)
		}
	}
}

// The legacy owner alias file widens who counts as the user: agents never
// name it (a speed bump; a script file gets around it).
func TestLegacyAliasFileProtection(t *testing.T) {
	for _, command := range []string{
		"echo mallory >> ~/.commander/legacy-owner-aliases", "tee -a $COMMANDER_HOME/legacy-owner-aliases",
		"cat legacy-owner-aliases", "sh -c 'printf x > /tmp/h/legacy-owner-aliases'",
	} {
		if !Classify(command, "").Deny {
			t.Errorf("allowed %s", command)
		}
	}
}
