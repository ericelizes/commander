package operatorguard

import "testing"

func TestCredentialGlobMatching(t *testing.T) {
	t.Setenv("HOME", "/tmp/user-home")
	t.Setenv("COMMANDER_HOME", "/tmp/private")
	for _, tc := range []struct {
		command string
		deny    bool
	}{
		{"cat l?near.key", true},
		{"cat [l]inear.key", true},
		{"cat lin[ae]ar.k?y", true},
		{"cat api-to?en", true},
		{"cat [a]pi-token", true},
		{"cat *", true},
		{"cat ~/.commander/l?near.key", true},
		{"cat ~/.commander/[l]inear.key", true},
		{"cat ~/.commander/lin[ae]ar.k?y", true},
		{"cat ~/.commander/api-to?en", true},
		{"cat ~/.commander/[a]pi-token", true},
		{"cat ~/.commander/*", true},
		{"cat $HOME/.commander/l?near.key", true},
		{"cat $HOME/.commander/[l]inear.key", true},
		{"cat $HOME/.commander/lin[ae]ar.k?y", true},
		{"cat $HOME/.commander/api-to?en", true},
		{"cat $HOME/.commander/[a]pi-token", true},
		{"cat $HOME/.commander/*", true},
		{"cat ${HOME}/.commander/l?near.key", true},
		{"cat ${HOME}/.commander/[l]inear.key", true},
		{"cat ${HOME}/.commander/lin[ae]ar.k?y", true},
		{"cat ${HOME}/.commander/api-to?en", true},
		{"cat ${HOME}/.commander/[a]pi-token", true},
		{"cat ${HOME}/.commander/*", true},
		{"cat $COMMANDER_HOME/l?near.key", true},
		{"cat $COMMANDER_HOME/[l]inear.key", true},
		{"cat $COMMANDER_HOME/lin[ae]ar.k?y", true},
		{"cat $COMMANDER_HOME/api-to?en", true},
		{"cat $COMMANDER_HOME/[a]pi-token", true},
		{"cat $COMMANDER_HOME/*", true},
		{"cat ${COMMANDER_HOME}/l?near.key", true},
		{"cat ${COMMANDER_HOME}/[l]inear.key", true},
		{"cat ${COMMANDER_HOME}/lin[ae]ar.k?y", true},
		{"cat ${COMMANDER_HOME}/api-to?en", true},
		{"cat ${COMMANDER_HOME}/[a]pi-token", true},
		{"cat ${COMMANDER_HOME}/*", true},
		{"cat /tmp/private/l?near.key", true},
		{"cat /tmp/private/[l]inear.key", true},
		{"cat /tmp/private/lin[ae]ar.k?y", true},
		{"cat /tmp/private/api-to?en", true},
		{"cat /tmp/private/[a]pi-token", true},
		{"cat /tmp/private/*", true},
		{"rg --files cmd/cmdr/linear*", false},
		{"cat /tmp/linear.key.example", false},
		{"cat /tmp/api-token.example", false},
		{"cat /repo/linear.go", false},
		{"ls cmd/cmdr/linear?", false},
		{"cat /repo/[l]inear*", false},
		{"cat ${HOME}/.commander/logs/ordinary.txt", false},
		{"cat /tmp/ordinary.txt", false},
		{"sh -c 'cat ${COMMANDER_HOME}/l?near.key'", true},
		{"cat /tmp/pri[v]ate/*", true},
	} {
		if v := Classify(tc.command); v.Deny != tc.deny {
			t.Errorf("%q: %+v, want deny=%v", tc.command, v, tc.deny)
		}
	}
}
