package managerguard

import "testing"

// TmuxKillBypasses are the review's (il9.89.3) bypasses of the first guard.
var tmuxKillBypasses = map[string]string{
	"sequence semicolon":   `tmux ls \; kill-server`,
	"sequence quoted ;":    `tmux ls ';' kill-server`,
	"sequence one arg":     `tmux 'ls ; kill-server'`,
	"prefix kill-serv":     "tmux kill-serv",
	"prefix kill-ser":      "tmux kill-ser",
	"prefix kill-sess -a":  "tmux kill-sess -a",
	"run-shell":            "tmux run-shell 'tmux kill-server'",
	"run pkill":            "tmux run 'pkill tmux'",
	"run-shell prefix":     "tmux run-s 'tmux kill-server'",
	"if-shell tmux cmd":    "tmux if-shell true kill-server",
	"if-shell shell cmd":   "tmux if-shell 'tmux kill-server' ls",
	"source-file":          "tmux source-file /tmp/x.conf",
	"display #( )":         "tmux display-message '#(tmux kill-server)'",
	"kill-window -t":       "tmux kill-window -t op-other",
	"kill-window -a":       "tmux kill-window -a",
	"killw alias":          "tmux killw -t op-other",
	"kill-pane -t":         "tmux kill-pane -t %5",
	"kill-pane -a":         "tmux kill-pane -a",
	"respawn-pane -k -t":   "tmux respawn-pane -k -t op-other",
	"send-keys exit":       "tmux send-keys -t op-other exit Enter",
	"send-keys C-d":        "tmux send-keys -t %3 C-d",
	"send alias":           "tmux send -t %3 exit Enter",
	"other server session": "tmux -L commander-ops kill-session",
	"other server window":  "tmux -L other kill-window",
	"kill pgrep ops":       "kill $(pgrep -f commander-ops)",
	"kill backtick pidof":  "kill -9 `pidof tmux`",
	"pgrep xargs":          "pgrep -f commander-ops | xargs kill",
	"pkill ops":            "pkill -f commander-ops",
	"eval":                 `eval "tmux kill-server"`,
	"chain ||":             "false || tmux kill-server",
	"chain after ls":       "tmux ls && tmux kill-sess -t x",
	"subshell parens":      "(tmux kill-server)",
	"brace group":          "{ tmux kill-server; }",
	"new-window":           "tmux new-window 'tmux kill-server'",
	"new-session":          "tmux new-session -d 'tmux kill-server'",
	"split-window":         "tmux split-window 'tmux kill-server'",
	"pipe-pane":            "tmux pipe-pane -t x 'tmux kill-server'",
	"set-hook":             "tmux set-hook -g after-new-window 'kill-server'",
	"bind-key":             "tmux bind-key K kill-server",
	"confirm-before":       "tmux confirm-before kill-server",
	"command-prompt":       "tmux command-prompt 'kill-server'",
	"display-menu":         "tmux display-menu x y kill-server",
	"respawn-window -k":    "tmux respawn-window -k -t op-other",
	"detach-client -a":     "tmux detach-client -a",
	"echo pipe sh":         "echo 'tmux kill-server' | sh",
	"printf pipe bash":     "printf 'tmux kill-server' | bash",
	"heredoc sh":           "sh <<EOF\ntmux kill-server\nEOF",
	"here-string":          "bash <<< 'tmux kill-server'",
	"base64 sh":            "echo dG11eCBraWxsLXNlcnZlcg== | base64 -d | sh",
	"xargs sh -c":          "echo x | xargs sh -c 'tmux kill-server' {}",
	"script -c":            "script -qc 'tmux kill-server' /dev/null",
	"python system":        "python3 -c \"import os; os.system('tmux kill-server')\"",
	"perl system":          "perl -e 'system(\"tmux kill-server\")'",
	"eval substitution":    `eval "$(echo tmux kill-server)"`,
	"ansi-c quoting":       "tmux $'kill-server'",
	"var subcommand":       "x=kill-server; tmux $x",
	"var tmux":             "T=tmux; $T kill-server",
	"subst subcommand":     "tmux $(echo kill-server)",
}

func TestTmuxKillBypasses(t *testing.T) {
	for name, c := range tmuxKillBypasses {
		if TmuxKill(c) == "" {
			t.Errorf("%s: %q was allowed by TmuxKill", name, c)
		}
		if v := Classify(c, ""); !v.Deny {
			t.Errorf("%s: %q was allowed by Classify", name, c)
		}
	}
}

func TestTmuxKillKeepsOwnSessionAndReads(t *testing.T) {
	for _, c := range []string{
		"tmux kill-session", "tmux kill-window", "tmux kill-pane",
		"tmux ls", "tmux -L commander-ops list-sessions", "tmux list-panes -a", "tmux capture-pane -p -t x",
		"tmux display-message -p '#{session_name}'", "tmux has-session -t x", "tmux show-options -g",
		"tmux send-keys -t x ls Enter", "tmux respawn-pane -t x", "tmux run-shell 'echo hi'",
		`tmux ls \; list-panes`, "echo tmux kill-server", "kill 1234",
	} {
		if why := TmuxKill(c); why != "" {
			t.Errorf("%q refused: %s", c, why)
		}
	}
}
