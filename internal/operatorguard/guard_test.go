package operatorguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		cmd  string
		deny bool
	}{
		{"go test ./...", true},
		{"go test -p 2 ./...", true},
		{"cd x && go test ./internal/... -count=1", true},
		{"go build ./...", true},
		{"go vet ./...", true},
		{"env -u TMUX go test ./...", true},
		{"GOFLAGS=-p=2 go test ./...", true},
		{"bash -c 'go test ./...'", true},
		{"echo $(go build ./...)", true},
		{"npm test", true},
		{"npm run test", true},
		{"cd app && npm test", true},
		{"npm test -- --run", true},
		{"npx vitest", true},
		{"npx vitest run", true},
		{"vitest run", true},
		{"npm run build", true},
		{"npm run dist -- --mac dir", true},
		{"npm --prefix app run build", true},
		{"npm run smoke", true},
		{"pytest", true},
		{"pytest -x", true},
		{"pytest tests/", true},
		{"pytest tests -x -q", true},
		{"cd sample_worker && pytest tests/ -x", true},
		{"pytest -k test_name", true},
		{".venv/bin/pytest", true},
		{"python -m pytest", true},
		{"python3 -m pytest tests/ -q", true},
		{"python3.13 -m pytest", true},
		{"uv run pytest", true},
		{"uv run --with pyyaml pytest -x", true},
		{"uv run python -m pytest", true},
		{"PYTHONPATH=sample_lib/python uv run pytest tests", true},
		{"poetry run pytest", true},
		{"just test", true},
		{"just -f sample_worker/Justfile test", true},
		{"ENV_FILE=.env.local just test", true},
		{"uvx pytest", true},
		{"bfs -S dfs -regextype findutils-default / -path *sample_lib* -name _agent_graph.py", true},
		{"find / -path *sample_lib*", true},
		{"find / -name foo", true},
		{"find /Users -name '*.go'", true},
		{"find ~ -name foo", true},
		{"find $HOME/ -type f", true},
		{"find " + filepath.Join("/Users", "example") + " -name x", true},
		{"fd foo /", true},
		{"mdfind sample_lib", true},
		{"rg foo /Users", true},
		{"rg foo ~", true},
		{"grep -rn foo /", true},
		{"grep -R foo $HOME", true},
		{"bash -c 'find / -name x'", true},
		{"ansible-playbook site.yml", true},
		{"molecule test", true},
		{"cargo test", true},
		{"cargo build --release", true},
		{"make test", true},
		{"docker build -t x .", true},

		{"cmdr internal-slot run -- go test ./...", false},
		{"cmdr internal-slot run --timeout 30m -- npm run build", false},
		{"lockf /tmp/cmdr-build.lock go test ./...", false},
		{"go test -p 2 ./internal/slot -run TestX", false},
		{"go test ./internal/slot", false},
		{"go test", false},
		{"go build ./cmd/cmdr", false},
		{"npm test -- src/foo.test.ts", false},
		{"npm run test -- features/chat/engine.test.ts", false},
		{"npx vitest run src/foo.test.ts", false},
		{"vitest run foo", false},
		{"npm run typecheck", false},
		{"npm ci", false},
		{"pytest tests/test_x.py", false},
		{"pytest tests/test_x.py::test_name -x", false},
		{"pytest -k foo tests/lib/test_inventory.py", false},
		{"pytest tests/test_x.py::TestA::test_b", false},
		{"python -m pytest tests/test_x.py::name", false},
		{"uv run pytest tests/test_x.py", false},
		{"uv run python -m pytest tests/test_x.py::name", false},
		{"cmdr internal-slot run -- uv run pytest", false},
		{"cmdr internal-slot run -- just test", false},
		{"python -m http.server", false},
		{"python script.py", false},
		{"uv run ruff check", false},
		{"just lint", false},
		{"just test tests/test_x.py::name", false},
		{"pip install pytest", false},
		{"find . -name foo", false},
		{"find internal -name '*.go'", false},
		{"find /tmp/example/code/commander/.worktrees/x -name y", false},
		{"find /tmp/cl.x -name y", false},
		{"fd foo", false},
		{"rg foo", false},
		{"rg foo internal/", false},
		{"rg / internal/", false},
		{"grep -rn / internal", false},
		{"grep -rn foo internal", false},
		{"grep foo /etc/hosts", false},
		{"grep -n foo /", false},
		{"mdfind -onlyin /tmp/example/code/commander foo", false},
		{"git grep foo", false},
		{"cargo check", false},
		{"make build", false},
		{"docker ps", false},
		{"git commit -m 'go test ./...'", false},
		{"echo go test ./...", false},
		{"br show x", false},
		{"", false},
	}
	for _, c := range cases {
		v := Classify(c.cmd)
		if v.Deny != c.deny {
			t.Errorf("Classify(%q) deny=%v, want %v (why %q)", c.cmd, v.Deny, c.deny, v.Why)
		}
	}
}

func TestHookOutputTellsToWrap(t *testing.T) {
	out := string(HookOutput("go test ./..."))
	for _, want := range []string{`"permissionDecision":"deny"`, "cmdr internal-slot run --", "go test ./..."} {
		if !strings.Contains(out, want) {
			t.Errorf("hook output missing %q: %s", want, out)
		}
	}
}

func TestScanVerdictHasOwnReason(t *testing.T) {
	v := Classify("find / -name x")
	if !v.Deny || !v.Scan {
		t.Fatalf("want a scan refusal, got %+v", v)
	}
	out := string(HookOutputFor(v))
	for _, want := range []string{"git grep", "named directory"} {
		if !strings.Contains(out, want) {
			t.Errorf("hook output missing %q: %s", want, out)
		}
	}
	if w := Classify("pytest tests"); w.Scan || !strings.Contains(string(HookOutputFor(w)), "internal-slot run") {
		t.Errorf("suite refusal should point at the slot: %+v", w)
	}
}

func TestClassify_RefusesTheInstallEndpoints(t *testing.T) {
	v := Classify(`curl -X POST -H "Authorization: Bearer $T" $U/api/update/install -d '{}'`)
	if !v.Deny || !v.Update {
		t.Fatalf("verdict = %+v", v)
	}
	if got := string(HookOutputFor(v)); !strings.Contains(got, "/api/update") || strings.Contains(got, "build slots") {
		t.Errorf("hook output = %s", got)
	}
	if v := Classify("curl $U/api/snapshot"); v.Deny {
		t.Errorf("snapshot read refused: %+v", v)
	}
}

func TestOperatorGuardTmuxRefusesKills(t *testing.T) {
	for _, c := range []string{
		"tmux kill-server", "tmux -L commander-ops kill-server", "tmux -S /tmp/x kill-server", "killall tmux", "pkill -f tmux",
		"kill $(pgrep tmux)", "tmux kill-session -a", "tmux kill-session -t op-other", "bash -c 'tmux kill-server'",
		"cd x && tmux -L commander-ops kill-server",
	} {
		v := Classify(c)
		if !v.Deny || !v.Tmux {
			t.Errorf("%q: want tmux deny, got %+v", c, v)
		}
		if !strings.Contains(string(HookOutputFor(v)), "tmux server") {
			t.Errorf("%q: reason does not explain tmux", c)
		}
	}
}

func TestTmuxKillOperatorBypasses(t *testing.T) {
	for _, c := range []string{
		`tmux ls \; kill-server`, "tmux kill-serv", "tmux kill-sess -a", "tmux run-shell 'tmux kill-server'",
		"tmux run 'pkill tmux'", "tmux if-shell true kill-server", "tmux source-file x", "tmux display-message '#(tmux kill-server)'",
		"tmux kill-window -t op-x", "tmux kill-pane -a", "tmux respawn-pane -k -t op-x", "tmux send-keys -t op-x exit Enter",
		"tmux -L other kill-session", "kill $(pgrep -f commander-ops)", "pkill -f commander-ops",
		"(tmux kill-server)", "tmux new-window 'tmux kill-server'", "tmux bind-key K kill-server",
		"tmux respawn-window -k -t op-x", "tmux detach-client -a", "echo 'tmux kill-server' | sh",
		"sh <<EOF\ntmux kill-server\nEOF", "echo dG11eA== | base64 -d | sh", "echo x | xargs sh -c 'tmux kill-server' {}",
		"python3 -c \"import os; os.system('tmux kill-server')\"", `eval "$(echo tmux kill-server)"`,
		"tmux $'kill-server'", "x=kill-server; tmux $x", "T=tmux; $T kill-server",
		`printf 'tmux\tkill-server\n' | sh`, `printf 'tmux ls\npkill tmux\n' | sh`, `printf %b 'tmux\x20kill-server' | sh`,
		`bash -c $'tmux\tkill-server'`, "python3 <<EOF\nimport os\nos.system('tmux\\tkill-server')\nEOF",
	} {
		if v := Classify(c); !v.Deny || !v.Tmux {
			t.Errorf("%q: want tmux deny, got %+v", c, v)
		}
	}
}

func TestOperatorGuardTmuxAllowsReadsAndOwnSession(t *testing.T) {
	for _, c := range []string{
		"tmux ls", "tmux -L commander-ops list-sessions", "tmux capture-pane -p -t x", "tmux list-panes -a",
		"tmux kill-session", "tmux display -p '#{session_name}'", "kill 1234", "go test -p 2 ./internal/x -run Y",
	} {
		if v := Classify(c); v.Deny {
			t.Errorf("%q refused: %+v", c, v)
		}
	}
}

func TestClassify_RefusesHandWrittenWaitLabels(t *testing.T) {
	v := Classify("br update p-1 --add-label w:tneb9u:un:tnebi0:repair-launch-check")
	if !v.Deny || !v.Wait {
		t.Fatalf("verdict = %+v", v)
	}
	if got := string(HookOutputFor(v)); !strings.Contains(got, "internal-set-objective --waiting") {
		t.Errorf("hook output = %s", got)
	}
	if v := Classify("br label remove p-1 waiting:1:user"); v.Wait {
		t.Errorf("removing a wait refused as a hand-written wait: %+v", v)
	}
}

func TestClassifyRefusesDirectBeadsWrites(t *testing.T) {
	t.Setenv("COMMANDER_AGENT_WRITE_GUARD", "")
	for cmd, verb := range map[string]string{
		"bd --db /r/.beads/beads.db comments add x-1 note":   "comments",
		"bd create t --parent x-1 --labels kind:operational": "create",
		"bd update x-1 --notes n":                            "update",
		"bd close x-1":                                       "close",
		"br dep add a b":                                     "dep",
		"bd label add x-1 a":                                 "label",
		"bd set-state x-1 cmdr=done":                         "set-state",
		"bash -c 'bd defer x-1'":                             "defer",
	} {
		v := Classify(cmd)
		if !v.Deny || !v.Beads || v.Verb != verb {
			t.Errorf("%q: %+v, want beads refusal for %s", cmd, v, verb)
		}
		if out := string(HookOutputFor(v)); !strings.Contains(out, "cmdr internal-") {
			t.Errorf("%q: reason does not name a cmdr verb: %s", cmd, out)
		}
	}
	for _, cmd := range []string{
		"bd --db /r/.beads/beads.db show x-1", "bd list --json", "bd comments x-1", "bd dep list x-1", "bd label list",
		"cmdr internal-ship op/x", "cmdr internal-comment --project p --bead b --text hi",
	} {
		if v := Classify(cmd); v.Deny {
			t.Errorf("%q refused: %+v", cmd, v)
		}
	}
	t.Setenv("COMMANDER_AGENT_WRITE_GUARD", "off")
	if v := Classify("bd close x-1"); v.Deny {
		t.Errorf("kill switch did not lift the refusal: %+v", v)
	}
}

func TestOperatorGuardRefusesGuardBypassNames(t *testing.T) {
	t.Setenv("COMMANDER_AGENT_WRITE_GUARD", "")
	v := Classify("COMMANDER_BR_HELD=1 echo hi")
	if !v.Deny || !v.Beads || v.Verb != "bypass" {
		t.Errorf("%+v", v)
	}
	if v := Classify("(bd close x-1)"); !v.Deny || v.Verb != "close" {
		t.Errorf("subshell: %+v", v)
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
		"cmdr linear set-key", "env X=y /tmp/cmdr linear set-key",
		"sh -c 'cmdr linear set-key'", "commander linear set-key",
	} {
		v := Classify(command)
		if !v.Deny || !v.Credential {
			t.Errorf("allowed or wrong reason %s: %+v", command, v)
		}
		if strings.Contains(string(HookOutputFor(v)), "internal-slot") {
			t.Error("wrong advice")
		}
	}
	if Classify("cmdr linear status").Deny {
		t.Error("status refused")
	}
}

func TestLegacyAliasFileProtection(t *testing.T) {
	for _, command := range []string{
		"echo mallory >> ~/.commander/legacy-owner-aliases", "tee -a $COMMANDER_HOME/legacy-owner-aliases",
	} {
		if v := Classify(command); !v.Deny {
			t.Errorf("allowed %s", command)
		}
	}
}

// Worktrees share one stash list: an Operator may not stash, pop, apply or drop (it could take
// another task's stash); reading the list is allowed.
func TestOperatorGuardRefusesGitStash(t *testing.T) {
	for _, c := range []string{
		"git stash", "git stash pop", "git stash apply stash@{0}", "git stash push -u -m wip", "git stash drop",
		"git -C /some/worktree stash pop", "cd x && git stash", "bash -c 'git stash pop'", "env GIT_DIR=x git stash",
		"git --attr-source HEAD stash pop", "git --super-prefix x/ stash", "git --namespace n stash apply", // m5
	} {
		v := Classify(c)
		if !v.Deny || !v.Stash {
			t.Errorf("%q not refused: %+v", c, v)
		}
		if out := string(HookOutputFor(v)); !strings.Contains(out, "WIP commit") {
			t.Errorf("%q: reason %s", c, out)
		}
	}
	for _, c := range []string{"git stash list", "git stash show -p", "git status", "git commit -m 'wip: stash this later'", "echo git stash"} {
		if v := Classify(c); v.Deny {
			t.Errorf("%q refused: %+v", c, v)
		}
	}
}
