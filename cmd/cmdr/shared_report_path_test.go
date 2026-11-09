package main

import "testing"

func TestSharedReportPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/tmp/report.md": true, "/tmp/../tmp/r.md": true, "/var/tmp/x": true,
		"/tmp/op.abc/report.md": false, "/work/tree/.report": false, "report.md": false,
	} {
		if got := sharedReportPath(p); got != want {
			t.Errorf("%s: got %v want %v", p, got, want)
		}
	}
}
