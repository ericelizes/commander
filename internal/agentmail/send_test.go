package agentmail

import (
	"context"
	"strings"
	"testing"
)

func TestSend_FullArgv_WithImportanceAndAck(t *testing.T) {
	var capturedArgs []string
	c := &cmdClient{
		exec: func(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
			capturedArgs = args
			return nil, nil, nil
		},
	}

	err := c.Send(context.Background(), "myproj", "lead-x", "operator-y",
		"hello", "body text",
		SendOptions{Importance: "urgent", AckRequired: true})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	want := []string{
		"mail", "send",
		"--project", "myproj",
		"--from", "lead-x",
		"--to", "operator-y",
		"--subject", "hello",
		"--body", "body text",
		"--importance", "urgent",
		"--ack-required",
		"--json",
	}
	if strings.Join(capturedArgs, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v\n want = %v", capturedArgs, want)
	}
}

func TestSend_OmitsImportanceWhenEmpty(t *testing.T) {
	var capturedArgs []string
	c := &cmdClient{
		exec: func(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
			capturedArgs = args
			return nil, nil, nil
		},
	}

	err := c.Send(context.Background(), "p", "a", "b", "s", "body", SendOptions{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	for _, a := range capturedArgs {
		if a == "--importance" {
			t.Errorf("--importance should be omitted when SendOptions.Importance is empty; got %v", capturedArgs)
		}
		if a == "--ack-required" {
			t.Errorf("--ack-required should be omitted when SendOptions.AckRequired is false; got %v", capturedArgs)
		}
	}
}

func TestSend_Validation(t *testing.T) {
	c := &cmdClient{exec: func(context.Context, string, ...string) ([]byte, []byte, error) {
		return nil, nil, nil
	}}
	cases := []struct {
		name, project, from, to, subject string
	}{
		{"empty project", "", "a", "b", "s"},
		{"empty from", "p", "", "b", "s"},
		{"empty to", "p", "a", "", "s"},
		{"empty subject", "p", "a", "b", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := c.Send(context.Background(), tc.project, tc.from, tc.to, tc.subject, "body", SendOptions{})
			if err == nil {
				t.Errorf("expected validation error, got nil")
			}
		})
	}
}
