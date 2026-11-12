package managerguard

import "testing"

// These strings are classifier input only; never execute them in a shell.
func TestSourceSearchEndpointData(t *testing.T) {
	for _, command := range []string{
		`rg -n '/api/update|install' internal`,
		`grep -R '/api/update' internal`,
		`git grep -n '/api/update' -- internal`,
		`git -C src --no-pager grep '/api/update'`,
		`rg 'curl localhost:8787 update' internal`,
		`rg '$(curl /api/update)' internal`,
	} {
		if TouchesUpdateAPI(command) || Classify(command, "").Deny {
			t.Errorf("inert search refused: %q", command)
		}
	}
	for _, command := range []string{
		`rg --pre 'curl /api/update' pattern`,
		`rg --hostname-bin 'curl /api/update' pattern`,
		`git -c core.pager='curl /api/update' grep pattern`,
		`git grep --open-files-in-pager='curl /api/update' pattern`,
		`rg "$(curl /api/update)" internal`,
		"rg '`curl /api/update`' internal | sh",
		`rg '/api/update' internal | python`,
		`rg '/api/update' internal > output`,
		`rg pattern internal; curl /api/update`,
	} {
		if !TouchesUpdateAPI(command) {
			t.Errorf("executable or unproven search allowed: %q", command)
		}
	}
}
