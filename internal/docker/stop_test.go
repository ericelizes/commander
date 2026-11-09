package docker

import (
	"context"
	"errors"
	"testing"
)

func TestStop_Argv(t *testing.T) {
	r := &recorder{}
	c := &cmdClient{exec: r.fn(), OS: "linux"}
	if err := c.Stop(context.Background(), "abc123"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	want := []string{"docker", "stop", "abc123"}
	if len(r.args) != len(want) {
		t.Fatalf("argv len = %d; want %d (%v)", len(r.args), len(want), r.args)
	}
	for i, a := range want {
		if r.args[i] != a {
			t.Errorf("argv[%d] = %q; want %q", i, r.args[i], a)
		}
	}
}

func TestStop_NoSuchContainer_NilError(t *testing.T) {
	r := &recorder{
		stderr: []byte("Error response from daemon: No such container: abc"),
		err:    errors.New("exit 1"),
	}
	c := &cmdClient{exec: r.fn(), OS: "linux"}
	if err := c.Stop(context.Background(), "abc"); err != nil {
		t.Errorf("Stop should swallow 'No such container'; got %v", err)
	}
}

func TestStop_NotRunning_NilError(t *testing.T) {
	r := &recorder{
		stderr: []byte("Error: container abc is not running"),
		err:    errors.New("exit 1"),
	}
	c := &cmdClient{exec: r.fn(), OS: "linux"}
	if err := c.Stop(context.Background(), "abc"); err != nil {
		t.Errorf("Stop should swallow 'not running'; got %v", err)
	}
}

func TestStop_OtherError_Wrapped(t *testing.T) {
	r := &recorder{
		stderr: []byte("permission denied"),
		err:    errors.New("exit 1"),
	}
	c := &cmdClient{exec: r.fn(), OS: "linux"}
	if err := c.Stop(context.Background(), "abc"); err == nil {
		t.Error("expected error for non-not-found stderr")
	}
}

func TestStop_EmptyArg(t *testing.T) {
	c := &cmdClient{exec: (&recorder{}).fn(), OS: "linux"}
	if err := c.Stop(context.Background(), ""); err == nil {
		t.Error("expected error for empty id")
	}
}
