package docker

import (
	"context"
	"errors"
	"testing"
)

func TestInspect_Argv_AndParse(t *testing.T) {
	payload := `{"id":"abc123","name":"/cmdr-proj-fb","image":"commander/operator:latest","status":"running","createdAt":"2026-05-09T10:00:00.123456789Z","labels":{"commander.project":"proj","commander.task":"t1"}}`
	r := &recorder{stdout: []byte(payload + "\n")}
	c := &cmdClient{exec: r.fn(), OS: "linux"}

	info, err := c.Inspect(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil ContainerInfo")
	}

	// argv check
	if len(r.args) < 5 || r.args[0] != "docker" || r.args[1] != "inspect" || r.args[2] != "abc123" || r.args[3] != "--format" {
		t.Errorf("unexpected argv: %v", r.args)
	}

	if info.ID != "abc123" {
		t.Errorf("ID = %q", info.ID)
	}
	if info.Name != "cmdr-proj-fb" {
		t.Errorf("Name should strip leading /, got %q", info.Name)
	}
	if info.Image != "commander/operator:latest" {
		t.Errorf("Image = %q", info.Image)
	}
	if info.Status != "running" {
		t.Errorf("Status = %q", info.Status)
	}
	if info.Labels["commander.project"] != "proj" {
		t.Errorf("missing project label: %v", info.Labels)
	}
	if info.CreatedAt.IsZero() {
		t.Error("CreatedAt should be parsed")
	}
	if info.CreatedAt.Year() != 2026 {
		t.Errorf("CreatedAt year = %d", info.CreatedAt.Year())
	}
}

func TestInspect_NotFound_NilNil(t *testing.T) {
	r := &recorder{
		stderr: []byte("Error: No such object: abc"),
		err:    errors.New("exit 1"),
	}
	c := &cmdClient{exec: r.fn(), OS: "linux"}
	info, err := c.Inspect(context.Background(), "abc")
	if err != nil {
		t.Errorf("expected nil error on not-found; got %v", err)
	}
	if info != nil {
		t.Errorf("expected nil info on not-found; got %+v", info)
	}
}

func TestInspect_NoSuchContainer_NilNil(t *testing.T) {
	r := &recorder{
		stderr: []byte("No such container: abc"),
		err:    errors.New("exit 1"),
	}
	c := &cmdClient{exec: r.fn(), OS: "linux"}
	info, err := c.Inspect(context.Background(), "abc")
	if err != nil {
		t.Errorf("expected nil error: %v", err)
	}
	if info != nil {
		t.Errorf("expected nil info: %+v", info)
	}
}

func TestInspect_OtherError_Wrapped(t *testing.T) {
	r := &recorder{
		stderr: []byte("permission denied"),
		err:    errors.New("exit 1"),
	}
	c := &cmdClient{exec: r.fn(), OS: "linux"}
	if _, err := c.Inspect(context.Background(), "abc"); err == nil {
		t.Error("expected wrapped error")
	}
}

func TestInspect_EmptyArg(t *testing.T) {
	c := &cmdClient{exec: (&recorder{}).fn(), OS: "linux"}
	if _, err := c.Inspect(context.Background(), ""); err == nil {
		t.Error("expected error for empty id")
	}
}
