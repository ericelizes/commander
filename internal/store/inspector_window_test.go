package store

import (
	"context"
	"testing"
)

func TestInspectorWindow(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pid, err := s.InsertProject(ctx, Project{Name: "example-app", Type: "code", Path: "/p", ActivatedAt: 1, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertWorkstreamManager(ctx, WorkstreamManager{ProjectID: pid, WorkstreamBeadID: "ws-1", SessionFile: "/f"}); err != nil {
		t.Fatal(err)
	}
	if id, ws, err := s.InspectorWindow(ctx, "", ""); err != nil || id != 0 || ws != "" {
		t.Errorf("commander window = %d %q %v", id, ws, err)
	}
	if id, ws, err := s.InspectorWindow(ctx, "example-app", "ws-1"); err != nil || id != pid || ws != "ws-1" {
		t.Errorf("by project = %d %q %v", id, ws, err)
	}
	if id, _, err := s.InspectorWindow(ctx, "", "ws-1"); err != nil || id != pid {
		t.Errorf("via manager row = %d %v", id, err)
	}
	for _, c := range [][2]string{{"example-app", ""}, {"nope", "ws-1"}, {"", "ws-unknown"}} {
		if _, _, err := s.InspectorWindow(ctx, c[0], c[1]); err == nil {
			t.Errorf("InspectorWindow(%q, %q) should fail", c[0], c[1])
		}
	}
}
