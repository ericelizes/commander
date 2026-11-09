package api

import "testing"

func TestAllowedShip(t *testing.T) {
	if !Allowed([]string{"internal-ship", "op/x", "--project", "p"}) {
		t.Error("internal-ship should be allowed")
	}
}

// A create made through /api/cmd prints internal-gate link as its finish command.
func TestAllowedGateLink(t *testing.T) {
	if !Allowed([]string{"internal-gate", "link", "--project", "p", "--bead", "b", "--after", "a"}) {
		t.Error("internal-gate link should be allowed")
	}
}

func TestGraphCheckAllowedOnlyAsCheck(t *testing.T) {
	if !Allowed([]string{"graph", "check", "--json"}) {
		t.Error("graph check should be allowed")
	}
	if Allowed([]string{"graph"}) || Allowed([]string{"graph", "fix"}) {
		t.Error("only graph check is allowed")
	}
}
