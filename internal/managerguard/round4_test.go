package managerguard

import "testing"

// Review il9.66.12.2 (round 3) findings: each must be denied from a worktree.
var round4Deny = map[string][]string{
	"1 sed writes": {
		"sed --i s/a/Z/ x", "sed --in s/a/Z/ x", "sed --in-p s/a/Z/ x", "sed -i s/a/Z/ x", "sed -ni p x",
		`sed 's|b|Q|w out' x`, `sed 's#a#b#w out' x`, `sed 'w/tmp/x' x`, "sed -n 'w${IFS}out' x", `sed -n 'w out' x`,
		`sed --exp='w out' x`, `sed --expr='w out' x`, "sed --fil s x", "sed --f s x", "sed -f s x", "sed -nf s x",
		`sed 's/a/b/e' x`, `sed 'e rm y' x`, `sed -e p -e 'w o' x`, `sed -ne 'w o' x`, `sed 'r /etc/passwd' x`,
	},
	"2 br comments add": {
		"br comments --json add x-1 hi", "br comments -C /x add x-1 hi", "br comments --actor a add x-1 hi", "br comments --db p add x-1 hi",
		"br comments add x-1 hi",
	},
	"3 home spellings": {
		"cd ~/.commander/. && cat *", "cd ~/.commander/logs && cat ../*", "cd ~/.commander/logs && cat ../a*", "cat ~/.commander/logs/../a*",
		"cat $COMMANDER_HOME/logs/../a*", "cd ~/.commander/logs/.. && cat *", "cd ~/.commander/logs; cd ..; cat *",
		"grep -r token ~", "grep -r x ~/.commander/.", "grep -r x ~/.commander/..", "grep -r token $HOME", "grep -r token /",
		"X=~/.commander; cat $X/*", "cat {.commander,x}/*", "cat .{commander,x}/*", "X=api; cat ~/.commander/${X}-token",
		"cat ~/.commander/a{p,q}i-token", "cat ~/.commander/api-{t,}oken", `cat ~/.commander/$'\x61pi-token'`, `cat ~/.commander/$'\141pi-token'`,
		"rg token ~/.commander/logs/..",
	},
	"4 gh api writes": {
		"gh api -XPOST x", "gh api -XDELETE x", "gh api --method=PATCH x", "gh api --method PUT x", "gh api --meth=POST x",
		"gh api x -fa=b", "gh api x --field=a=b", "gh api x --raw-field=a=b", "gh api x --input=f", "gh api x -Fa=@f",
	},
	"5 file abbreviations": {"file --co x", "file --comp x", "file --compil x"},
	"6 uniq out":           {"uniq - out", "uniq -c - out"},
	"7 exec via env": {
		"X='$(rm y)'; echo ${X@P}", "export PATH=./x:$PATH; git log", "export BASH_ENV=f; git log", "export LD_PRELOAD=x; ls", "export GIT_PAGER=sh; git log",
	},
	"8 git abbreviations": {"git grep --open x", "git grep --open-files x", "git ls-remote --upload=sh .", "git log --outp=f", "git grep --exe=sh x"},
}

var round4Allow = []string{
	"sed -n '1,5p' f", "sed -n '/foo/p' f", "sed -n '$p' f", "sed 's/a/b/' f", "sed 's/a/b/g;s/c/d/gI' f", "sed -E 's|a|b|g' f", "sed -ne '2,3p' f",
	"sed -e 's/a/b/' -e 'p' f", "sed '/x/d' f", "sed --quiet --expression='1p' f", "sed -n '/a/,/b/p' f", "sed '1!G;h;$!d' f", "sed 's/w/e/' f",
	"br comments x-1", "br comments --json x-1", "br comments -C /x x-1",
	"cat ~/.commander/logs/$X.jsonl", "cat ~/.commander/logs/x{a,b}.jsonl", "grep -r x ~/.commander/logs", "cd ~/.commander/logs && cat *.jsonl",
	"cd /tmp && cat *.txt", "cat ../*", "for f in a b; do cat $f; done", "cat $F", "grep -r x .", "grep -r x /tmp",
	"gh api repos/o/r/pulls", "gh api -X GET repos/o/r", "gh api -XGET x", "gh api --method=GET x", "gh api graphql -f query='query { viewer { login } }'",
	"uniq f", "uniq -c f", "sort a | uniq -", "file f", "file -b f", "export FOO=bar", "echo ${X}", "git log --oneline", "git grep -n foo", "git log --output-indicator-new=+",
}

func TestManagerGuardRound4Denies(t *testing.T) {
	for item, cmds := range round4Deny {
		cwds := []string{"/repo/.worktrees/op-x", "/repo/.worktrees.noindex/op-x"}
		if item[0] == '3' {
			cwds = append(cwds, "", "/tmp")
		}
		for _, c := range cmds {
			for _, cwd := range cwds {
				if v := Classify(c, cwd); !v.Deny {
					t.Errorf("item %s: %q allowed (cwd %q)", item, c, cwd)
				}
			}
		}
	}
}

func TestManagerGuardRound4Allows(t *testing.T) {
	for _, c := range round4Allow {
		for _, cwd := range []string{"", "/repo/.worktrees/op-x", "/repo/.worktrees.noindex/op-x"} {
			if v := Classify(c, cwd); v.Deny {
				t.Errorf("%q denied (cwd %q): %s", c, cwd, v.Why)
			}
		}
	}
}
