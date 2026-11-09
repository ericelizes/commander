package managerguard

import (
	"strings"
	"testing"
)

func TestLinearStatusReadOnly(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"linear", "status"}, true},
		{[]string{"linear", "set-key"}, false},
		{[]string{"linear", "status", "set-key"}, false},
	} {
		if got := Classify("cmdr "+strings.Join(tc.args, " "), ""); got.Deny == tc.want {
			t.Fatalf("classification %v: %+v", tc.args, got)
		}
		if got := pureCmdr(tc.args); got != tc.want {
			t.Fatalf("%v: %v", tc.args, got)
		}
	}
}
