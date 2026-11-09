package livedata

import "testing"

func TestLegacyApproverIsUser(t *testing.T) {
	a := Approvers([]string{"gate", "approver:user", "approver:alice"})
	if len(a) != 2 || a[0] != "user" || a[1] != "alice" {
		t.Fatalf("%v", a)
	}
}
