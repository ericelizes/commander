package managerguard

import "testing"

// Review il9.66.10 (round 2) findings: each must be denied from every cwd below.
var round3Deny = map[string][]string{
	"1 tmux sequences": {
		`tmux ls \; run-shell 'touch f'`, `tmux display-message -p x \; run 'touch f'`, `tmux capture-pane -p -t x \; send-keys x`,
		`tmux has-session \; kill-server`, `tmux has-session ';' kill-server`, `tmux ls\; kill-server`,
	},
	"2 rg runs programs": {"rg --pre sh x", "rg --pre=sh x", "rg --hostname-bin=sh x", "rg --hostname-bin sh x"},
	"3 git is an allow-list": {
		"git checkout x", "git switch x", "git restore f", "git reset --hard", "git clean -fd", "git branch newbranch", "git branch -D x",
		"git branch -m a b", "git worktree add ../x", "git worktree remove x", "git stash", "git stash pop", "git stash drop",
		"git cat-file --filters HEAD:f", "git for-each-ref --output=f", "git grep -O sh x", "git grep --open-files-in-pager=sh x",
		"git symbolic-ref HEAD refs/heads/x", "git reflog expire --all", "git tag x", "git update-ref -d refs/x", "git apply p",
	},
	"6 file writes": {
		"uniq a b", "uniq -c a out", "sort --out f a", "sort --outp=f a", "sort --output=f a", "sort --compress-prog=sh a",
		"sed -n 'W f' a", "time -o f ls", "time --output=f ls", "file -C", "file --compile",
	},
	"8 token by glob or recursion": {
		"cd ~/.commander; cat a*", "cd ~/.commander && cat ./a?i-token", "grep -r token ~/.commander", "grep -r . ~/.commander",
		"rg token ~/.commander", "cat ~/.commander/a*", "cat $HOME/.commander/*", "head $COMMANDER_HOME/a*", "jq . ~/.commander/*",
	},
}

var round3Allow = []string{
	"git log --oneline -5", "git status --short", "git diff HEAD~1", "git show HEAD", "git branch", "git branch -a", "git branch --list 'op/*'",
	"git branch --contains abc1234", "git branch --no-merged main", "git branch --show-current", "git cherry main op/x", "git worktree list",
	"git rev-parse HEAD", "git ls-files", "git stash list", "git stash show", "git blame f", "git symbolic-ref -q HEAD", "git -C .worktrees/op-x status",
	"br show x-1", "br list --json", "br comments x-1", "br -C /repo show x-1", "br -C /repo list | head", "br --db /x/.beads/b.db list",
	"tmux ls", "tmux capture-pane -p -t x", "tmux has-session -t x", "ps aux | grep cmdrd", "pgrep -fl claude",
	"ls -la", "cat go.mod", "head -3 f", "tail -5 f", "grep -n x f", "jq . f.json", "date", "wc -l f", "rg foo", "uniq f", "uniq -c f",
	"sort a | uniq -c", "sort -n f", "sed -n '1,5p' f", "time ls", "file f", "git log | head -3 | wc -l",
	"cat ~/.commander/logs/x.jsonl", "grep -r x ~/.commander/logs", "cat ~/.commander/logs/*.jsonl",
}

func TestManagerGuardRound3Denies(t *testing.T) {
	// Outside a worktree the usual rules apply (these are not all refused there); item 8 is refused everywhere.
	cwds := []string{"/repo/.worktrees/op-x", "/repo/.worktrees.noindex/op-x"}
	for item, cmds := range round3Deny {
		for _, c := range cmds {
			for _, cwd := range append(cwds, "", "/tmp")[:map[bool]int{true: 4, false: 2}[item[0] == '8']] {
				if v := Classify(c, cwd); !v.Deny {
					t.Errorf("item %s: %q allowed (cwd %q)", item, c, cwd)
				}
			}
		}
	}
	// In the commander home itself, a bare glob or recursive search can reach the token.
	for _, c := range []string{"cat a*", "cat ./a?i-token", "grep -r token", "grep -r . .", "rg token"} {
		if v := Classify(c, "/opt/u/.commander"); !v.Deny {
			t.Errorf("item 8: %q allowed in the commander home", c)
		}
	}
}

func TestManagerGuardRound3Allows(t *testing.T) {
	for _, c := range round3Allow {
		for _, cwd := range []string{"", "/repo/.worktrees/op-x", "/repo/.worktrees.noindex/op-x"} {
			if v := Classify(c, cwd); v.Deny {
				t.Errorf("%q denied (cwd %q): %s", c, cwd, v.Why)
			}
		}
	}
}

// Item 5: .worktrees.noindex counts as a worktree for non-read commands.
func TestManagerGuardRound3NoindexIsAWorktree(t *testing.T) {
	for _, c := range []string{"br close a", "git checkout x", "xargs rm", "python3 -c 'print(1)'", "cmdr internal-ship b"} {
		if v := Classify(c, "/repo/.worktrees.noindex/op-x"); !v.Deny {
			t.Errorf("%q allowed from .worktrees.noindex", c)
		}
	}
}

// Item 4: TmuxKill still runs before the pure-read rule and sees ';' chains.
func TestManagerGuardRound3TmuxKillChains(t *testing.T) {
	for _, c := range []string{`tmux has-session \; kill-server`, `tmux ls \; kill-session -t op-x`, `tmux ls \; kill-session -a`, `tmux has-session ';' kill-server`, `tmux ls\; kill-server`} {
		if TmuxKill(c) == "" {
			t.Errorf("TmuxKill missed %q", c)
		}
		if v := Classify(c, ""); !v.Deny {
			t.Errorf("%q allowed", c)
		}
	}
	if TmuxKill(`tmux ls \; list-panes -a`) != "" {
		t.Error("read-only chain refused by TmuxKill")
	}
}
