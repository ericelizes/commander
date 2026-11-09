package operatorguard

import "testing"

func TestExecutedCommandsDistinguishReportData(t *testing.T) {
	for _, tc := range []struct {
		command string
		deny    bool
	}{
		{`rg 'pytest tmux kill-server create' internal`, false},
		{`cmdr internal-comment --text 'pytest tmux kill-server create'`, false},
		{`cat <<'EOF'
pytest; tmux kill-server; bd create issue
EOF`, false},
		{`cat <<'EOF'
$(pytest)
EOF`, false},
		{`cat <<EOF
$(pytest)
EOF`, true},
		{`sh <<'EOF'
pytest
EOF`, true},
		{`sh -e <<'EOF'
pytest
EOF`, true},
		{`sh -eu <<'EOF'
pytest
EOF`, true},
		{`bash -o errexit <<'EOF'
pytest
EOF`, true},
		{`bash -s -- script-argument <<'EOF'
pytest
EOF`, true},
		{`dash -eu <<'EOF'
pytest
EOF`, true},
		{`zsh -eu <<'EOF'
pytest
EOF`, true},
		{`cat <<'EOF'
sh -eu; pytest
EOF`, false},
		{`sh -eu <<'EOF'
printf '%s' 'pytest create'
EOF`, false},
		{`dash -c 'pytest'`, true},
		{`sh -eu <<< 'pytest'`, true},
		{`cat <<< 'pytest'`, false},
		{`cat <<'EOF' | sh
bd create issue
EOF`, true},
		{`printf '%s' "$(pytest)"`, true},
		{`sh -c 'tmux kill-server'`, true},
		{`pytest --help`, false},
		{`pytest --help; pytest`, true},
		{`cat ~/.commander/api-token`, true},
	} {
		t.Run(tc.command, func(t *testing.T) {
			if got := Classify(tc.command); got.Deny != tc.deny {
				t.Fatalf("got %+v want deny=%v", got, tc.deny)
			}
		})
	}
}
