package managerguard

import "testing"

// Text that merely mentions the phrase (commit messages, bead comments,
// searches) must pass; review il9.89.5.6 findings 1 and 2.
func TestTmuxKillAllowsTextThatMentionsIt(t *testing.T) {
	for name, c := range map[string]string{
		"commit heredoc":           "git commit -m \"$(cat <<'EOF'\nfix: tmux kill guard no longer refuses text\n\nCo-Authored-By: x\nEOF\n)\"",
		"comment heredoc":          "br comments add X <<EOF\nthe tmux kill-server rule is a speed bump\nEOF",
		"cat to file":              "cat > notes.md <<EOF\ntmux kill-server is refused\nEOF",
		"evaluation":               "git commit -m 'tmux kill guard evaluation' -m \"$(date)\"",
		"git grep":                 "git grep -n 'tmux kill-server' -- docs/python",
		"rg":                       "rg 'tmux kill' internal node",
		"heredoc then interpreter": "cat <<'EOF'\ntmux kill-server\nEOF\npython3 tools/read.py",
		"decoder read then shell":  "base64 --help; sh -c 'echo ok'",
		"git log grep":             "git log --grep='tmux kill' -- script",
		"and chain":                "git commit -m 'tmux kill guard' && bash scripts/x.sh",
		"semicolon chain":          "echo 'tmux kill guard'; python3 tools/x.py",
	} {
		if why := TmuxKill(c); why != "" {
			t.Errorf("%s: %q refused: %s", name, c, why)
		}
	}
}

// A line naming tmux with kill only inside another word is no kill, whatever it reaches.
func TestTmuxKillAllowsKillInsideAnotherWord(t *testing.T) {
	for name, c := range map[string]string{
		"python heredoc": "python3 <<EOF\n# the tmux test uses the review skill\nEOF",
		"sh -c":          "sh -c 'tmux ls; ls skills'",
		"piped to sh":    "echo 'tmux ls; cat SKILL.md' | sh",
		"variable":       "$T ls skills/tmux",
		"written file":   "echo 'tmux ls # overkill' > x.sh && sh x.sh",
		"after escape":   `printf 'tmux ls\nskills\n' | sh`,
		"after hex":      `printf 'tmux ls\x20overkill' | sh`,
	} {
		if why := TmuxKill(c); why != "" {
			t.Errorf("%s: %q refused: %s", name, c, why)
		}
	}
}

// A kill word that starts a word stays refused, whatever follows it.
func TestTmuxKillStillRefusesKillWords(t *testing.T) {
	for name, c := range map[string]string{
		"hyphen":     "python3 <<EOF\nrun('tmux', 'kill-server')\nEOF",
		"method":     "python3 <<EOF\n# tmux\nos.kill(pid, 9)\nEOF",
		"underscore": "python3 <<EOF\nimport tmux\ns.kill_session()\nEOF",
		"camel":      "node <<EOF\ntmux.killServer()\nEOF",
		"killall":    "echo 'killall tmux' | sh",
		"pkill":      "echo 'pkill tmux' | sh",
		"sigkill":    "python3 <<EOF\n# tmux\nos.killpg(p, signal.SIGKILL)\nEOF",
		"upper":      "bash <<EOF\nT=tmux\nKILL=1\nEOF",
	} {
		if TmuxKill(c) == "" {
			t.Errorf("%s: %q was allowed", name, c)
		}
	}
}

// An escape starts a word: the letter or digit right before the kill word
// belongs to \t, \n, \x20 or \040, which the shell or interpreter turns into a
// separator. Review il9.163.2.
func TestTmuxKillRefusesKillWordAfterEscape(t *testing.T) {
	for name, c := range map[string]string{
		"printf tab":       `printf 'tmux\tkill-server\n' | sh`,
		"printf newline":   `printf 'tmux ls\npkill tmux\n' | sh`,
		"printf killall":   `printf 'true\nkillall tmux\n' | sh`,
		"printf %b hex":    `printf %b 'tmux\x20kill-server\n' | sh`,
		"printf %b octal":  `printf %b 'tmux\040kill-server\n' | sh`,
		"printf %b 4 oct":  `printf %b 'tmux\0040kill-server\n' | sh`,
		"printf %b u":      `printf %b 'tmux kill-server\n' | sh`,
		"printf %b U":      `printf %b 'tmux\U00000020kill-server\n' | sh`,
		"echo -e":          `echo -e 'tmux\tkill-server' | sh`,
		"echo -e pkill":    `echo -e 'tmux ls\npkill tmux' | bash`,
		"bash -c ansi":     `bash -c $'tmux\tkill-server'`,
		"sh -c hex":        `sh -c $'tmux\x20kill-server'`,
		"eval ansi":        `eval $'tmux\tkill-server'`,
		"eval printf":      `eval "$(printf 'tmux\tkill-server')"`,
		"here-string":      `sh <<< $'tmux\tkill-server'`,
		"python os.system": `python3 -c "import os;os.system('tmux ls\npkill tmux')"`,
		"python tab":       `python3 -c "import os;os.system('tmux\tkill-server')"`,
		"python heredoc":   "python3 <<EOF\nimport os\nos.system('tmux\\tkill-server')\nEOF",
		"perl":             `perl -e 'system("tmux\tkill-server")'`,
		"ruby":             `ruby -e 'system("tmux\tkill-server")'`,
		"node":             `node -e 'cp.execSync("tmux\tkill-server")'`,
		"written file":     `printf 'tmux\tkill-server\n' > x.sh && sh x.sh`,
		"upper case":       `printf %b 'TMUX\X20KILL-SERVER' | sh`,
		"sigkill":          `printf 'tmux ls\nSIGKILL\n' | sh`,
	} {
		if TmuxKill(c) == "" {
			t.Errorf("%s: %q was allowed", name, c)
		}
	}
}

// The same shapes with a real sink stay refused.
func TestTmuxKillStillRefusesSinks(t *testing.T) {
	for name, c := range map[string]string{
		"decoder substitution": `echo "$(base64 -d payload | sh)"`,
		"heredoc sh":           "sh <<EOF\ntmux kill-server\nEOF",
		"heredoc env sh":       "env X=1 bash <<EOF\ntmux kill-server\nEOF",
		"cat pipe sh":          "cat <<EOF | sh\ntmux kill-server\nEOF",
		"written file":         "echo 'tmux kill-server' > x.sh && sh x.sh",
		"sudo -u":              "echo 'tmux kill-server' | sudo -u root sh",
		"timeout":              "echo 'tmux kill-server' | timeout 5 sh",
		"nice -n":              "echo 'tmux kill-server' | nice -n 5 sh",
		"xargs -I":             "echo 'tmux kill-server' | xargs -I{} sh -c {}",
		"sudo bash -s":         "echo 'tmux kill-server' | sudo -u root bash -s",
		"brace group":          "echo 'tmux kill-server' | { sh; }",
		"paren group":          "echo 'tmux kill-server' | ( sh )",
		"busybox":              "echo 'tmux kill-server' | busybox sh",
		"stdbuf":               "echo 'tmux kill-server' | stdbuf -o0 sh",
		"doas":                 "echo 'tmux kill-server' | doas sh",
		"ionice":               "echo 'tmux kill-server' | ionice -c3 sh",
		"and chain sink":       "git status && echo 'tmux kill-server' | bash",
	} {
		if TmuxKill(c) == "" {
			t.Errorf("%s: %q was allowed", name, c)
		}
	}
}
