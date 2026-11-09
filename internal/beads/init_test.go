package beads

import (
	"context"
	"errors"
	"testing"
)

func TestInit_RunsBrInit(t *testing.T) {
	var got []string
	c := &cmdClient{run: captureExec(&got, []byte("Initialized beads workspace\n"), nil, nil)}
	if err := c.Init(context.Background(), "/code/p"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	assertArgv(t, got, []string{"br", "init", "cwd=/code/p"})
}

func TestInit_AlreadyInitializedIsNoOp(t *testing.T) {
	// br init exits non-zero when the workspace exists; treat as success.
	stderr := []byte("Error: Already initialized at './.beads/beads.db'\n")
	c := &cmdClient{run: captureExec(new([]string), nil, stderr, errors.New("exit status 1"))}
	if err := c.Init(context.Background(), "/code/p"); err != nil {
		t.Errorf("Init on already-initialized workspace should be a no-op, got: %v", err)
	}
}

func TestInit_RealErrorPropagates(t *testing.T) {
	c := &cmdClient{run: captureExec(new([]string), nil, []byte("permission denied"), errors.New("exit status 1"))}
	if err := c.Init(context.Background(), "/code/p"); err == nil {
		t.Error("expected a real br init error to propagate")
	}
}

func TestInit_RequiresProjectDir(t *testing.T) {
	c := &cmdClient{run: captureExec(new([]string), nil, nil, nil)}
	if err := c.Init(context.Background(), ""); err == nil {
		t.Error("expected error for empty projectDir")
	}
}
