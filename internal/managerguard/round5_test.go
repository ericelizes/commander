package managerguard

import "testing"

// Review il9.66.12.4 (round 4) findings: each must be denied from a worktree and elsewhere.
var round5Deny = map[string][]string{
	"1 exec via expansion": {
		"PS4='$(rm y)'; set -x; ls", "PS4='$(rm y)'; set -o xtrace; ls", "x='a[$(rm y)]'; [[ $x -eq 1 ]]", "x='a[$(rm y)]'; [[ -v $x ]]",
		"printf -v 'a[$(rm y)]' x", "declare -n r='x[$(rm y)]'; echo $r", "x='$(rm y)'; echo $x", "x='`rm y`'; echo $x", "export x='a[1]'",
	},
	"2 home glob and recursion": {
		"grep -r token ~/.[c]*", "grep -r t ~/.comm*", "cd ~ && cat .com*/a*", "cd ~ && grep -r t", "cd ~ && rg t", "cd ~ && grep -r t .*",
		"grep -r t ~/..", "grep -r t /home/*", "grep -r t /Users", "grep -r t ~root", "grep -r t /home", "cat ~/.*/api-token", "ls ~/.com*",
		"cd ~/.commander && rev *", "cd ~/.commander && diff * x", "cd ~/.commander && comm a* b", "cd ~/.commander && column *",
		"cd ~/.commander && tr a b < a*", "cd ~/.commander && paste *", "cd ~/.commander && fold *", "cat /home/*/.commander/api-t*",
	},
	"3 gh clusters": {"gh api -iXPOST x", "gh api -iXDELETE x", "gh api -sXPUT x", "gh api -ifa=b x", "gh api -iX POST x"},
}

var round5Allow = []string{
	"s=$(tmux ls | cut -d: -f1); echo $s", "x=1; echo $x", "export FOO=bar", "cat *.txt", "ls *.go", "grep -r foo .", "grep -r foo", "rg foo", "rg foo src",
	"cat ~/.commander/logs/*.jsonl", "grep -n 'a\\[1\\]' f", "diff a b", "rev f", "tr a b < f", "gh api -i repos/o/r", "gh api -iXGET x", "gh api -i x",
	"set -e", "printf '%s' x", "declare -a arr",
}

func TestManagerGuardRound5Denies(t *testing.T) {
	for item, cmds := range round5Deny {
		for _, c := range cmds {
			cwds := []string{"/repo/.worktrees/op-x", "/repo/.worktrees.noindex/op-x"}
			if item[0] != '1' { // item 1 is only refused on the pure-read path (worktrees)
				cwds = append(cwds, "", "/tmp")
			}
			for _, cwd := range cwds {
				if v := Classify(c, cwd); !v.Deny {
					t.Errorf("item %s: %q allowed (cwd %q)", item, c, cwd)
				}
			}
		}
	}
}

func TestManagerGuardRound5Allows(t *testing.T) {
	for _, c := range round5Allow {
		for _, cwd := range []string{"", "/repo/.worktrees/op-x", "/repo/.worktrees.noindex/op-x"} {
			if v := Classify(c, cwd); v.Deny {
				t.Errorf("%q denied (cwd %q): %s", c, cwd, v.Why)
			}
		}
	}
}
