package operatorguard_test

import (
	m "github.com/example-user/commander/internal/managerguard"
	o "github.com/example-user/commander/internal/operatorguard"
	"strings"
	"testing"
)

// Data-only regression corpus from independent reviewer operator-exec-vm-10-744c94ecfb0f.
// Commands are classified as strings and are never executed.
func TestHeredocReview83(t *testing.T) {
	endpoint := strings.Join([]string{"http:", "", "127.0.0.1:8787", "api", "update", "install"}, "/")
	req := "curl -X POST " + endpoint
	type Case struct {
		Name, Command string
		Want          bool
	}
	cases := []Case{
		{"direct", req, true},
		{"unquoted-data", "cat <<EOF\n" + req + "\nEOF\n", false},
		{"quoted-data", "cat <<'EOF'\n" + req + "\nEOF\n", false},
		{"double-quoted-data", "cat <<\"EOF\"\n" + req + "\nEOF\n", false},
		{"unquoted-following", "cat <<EOF\ndata\nEOF\n" + req, true},
		{"quoted-following", "cat <<'EOF'\ndata\nEOF\n" + req, true},
		{"escaped-following", "cat <<\\EOF\ndata\nEOF\n" + req, true},
		{"concatenated-following", "cat <<E'OF'\ndata\nEOF\n" + req, true},
		{"tabs-following", "cat <<-'EOF'\n\tdata\n\tEOF\n" + req, true},
		{"tabs-escaped-following", "cat <<-\\EOF\n\tdata\n\tEOF\n" + req, true},
		{"here-string-data", "cat <<< '" + req + "'", false},
		{"here-string-sh", "sh <<< '" + req + "'", true},
		{"here-string-python", "python3 <<< '" + req + "'", true},
		{"sh-input", "sh <<'EOF'\n" + req + "\nEOF\n", true},
		{"bash-input", "bash <<'EOF'\n" + req + "\nEOF\n", true},
		{"python-input", "python3 <<'EOF'\nimport urllib.request\nurllib.request.urlopen('" + endpoint + "',data=b'')\nEOF\n", true},
		{"pipe-sh", "cat <<'EOF' | sh\n" + req + "\nEOF\n", true},
		{"pipe-bash", "cat <<'EOF' | bash\n" + req + "\nEOF\n", true},
		{"pipe-python", "cat <<'EOF' | python3\nimport urllib.request\nurllib.request.urlopen('" + endpoint + "',data=b'')\nEOF\n", true},
		{"dollar-sub", "cat <<EOF\n$(" + req + ")\nEOF\n", true},
		{"backtick-sub", "cat <<EOF\n`" + req + "`\nEOF\n", true},
		{"quoted-sub-data", "cat <<'EOF'\n$(" + req + ")\nEOF\n", false},
		{"multiple-following", "cat <<'ONE' <<TWO\ndata\nONE\ndata\nTWO\n" + req, true},
		{"multiple-second-sub", "cat <<'ONE' <<TWO\ndata\nONE\n$(" + req + ")\nTWO\n", true},
		{"multiple-escaped-following", "cat <<'ONE' <<\\TWO\ndata\nONE\ndata\nTWO\n" + req, true},
		{"ansi-direct", "curl -X POST $'" + strings.Replace(endpoint, "api", "\\x61pi", 1) + "'", true},
		{"source-search", "rg '" + endpoint + "' internal/managerguard", false},
		{"unterminated-quoted", "cat <<'EOF'\n" + req, true},
		{"unterminated-unquoted", "cat <<EOF\n" + req, true},
		{"uncertain-ansi-delimiter", "cat <<$'\\x45OF'\ndata\nEOF\n" + req, true},
		{"unclosed-delimiter-quote", "cat <<'EOF\n" + req, true},
		{"escaped-data", "cat <<\\EOF\n" + req + "\nEOF", false},
		{"concatenated-data", "cat <<E'OF'\n" + req + "\nEOF", false},
		{"quoted-space-delimiter", "cat <<'END HERE'\n" + req + "\nEND HERE", false},
		{"double-quote-backslash", "cat <<\"E\\OF\"\n" + req + "\nE\\OF", false},
		{"mixed-delimiter", "cat <<E\"O\"\\F\n" + req + "\nEOF", false},
		{"space-is-not-terminator", "cat <<'EOF'\ndata\n EOF\n" + req, true},
		{"delimiter-line-continuation", "cat <<\\\nEOF\n$(" + req + ")\nEOF", true},
	}

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			if got := m.TouchesUpdateAPI(c.Command); got != c.Want {
				t.Errorf("TouchesUpdateAPI = %v, want %v", got, c.Want)
			}
			if got := m.Classify(c.Command, "").Deny; got != c.Want {
				t.Errorf("manager deny = %v, want %v", got, c.Want)
			}
			if got := o.Classify(c.Command).Deny; got != c.Want {
				t.Errorf("operator deny = %v, want %v", got, c.Want)
			}
		})
	}
}
