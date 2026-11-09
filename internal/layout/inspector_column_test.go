package layout

import "testing"

func TestRightColumnClassification(t *testing.T) {
	for _, cmd := range []string{
		"cmdr __column inspector",
		"cmdr __column inspector --ws example-app-t11 --project example-app",
		"cmdr __column cmdr-fleet", // windows built before the inspector
	} {
		if !isRightContextColumn(cmd) {
			t.Errorf("isRightContextColumn(%q) = false", cmd)
		}
		if !isRightColumn(cmd) {
			t.Errorf("isRightColumn(%q) = false; Relayout would not size it", cmd)
		}
	}
	for _, cmd := range []string{"cmdr __column cmdr-objectives", "cmdr __column operator-side --task t-1", "claude -n manager"} {
		if isRightContextColumn(cmd) {
			t.Errorf("isRightContextColumn(%q) = true", cmd)
		}
	}
}
