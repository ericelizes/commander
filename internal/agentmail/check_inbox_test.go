package agentmail

import (
	"context"
	"strings"
	"testing"
)

// TestCheckInbox_AlwaysRateLimitZero is the regression guard for the verified
// am discrepancy: default --rate-limit is 120s, which makes polling unusable.
// Commander always passes `--rate-limit 0`.
func TestCheckInbox_AlwaysRateLimitZero(t *testing.T) {
	var capturedArgs []string
	c := &cmdClient{
		exec: func(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
			capturedArgs = args
			return []byte("[]"), nil, nil
		},
	}

	if _, err := c.CheckInbox(context.Background(), "myproj", "operator-x"); err != nil {
		t.Fatalf("CheckInbox: %v", err)
	}

	if v := valueFor(capturedArgs, "--rate-limit"); v != "0" {
		t.Errorf("--rate-limit = %q, want 0 (am default 120s makes polling unusable)", v)
	}
}

func TestCheckInbox_FullArgv(t *testing.T) {
	var binName string
	var capturedArgs []string
	c := &cmdClient{
		exec: func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
			binName = name
			capturedArgs = args
			return []byte("[]"), nil, nil
		},
	}

	if _, err := c.CheckInbox(context.Background(), "myproj", "lead-myproj"); err != nil {
		t.Fatalf("CheckInbox: %v", err)
	}

	if binName != "am" {
		t.Errorf("binary = %q, want am", binName)
	}

	want := []string{
		"check-inbox",
		"--project", "myproj",
		"--agent", "lead-myproj",
		"--rate-limit", "0",
		"--json",
	}
	if strings.Join(capturedArgs, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v\n want = %v", capturedArgs, want)
	}
}

func TestCheckInbox_ParsesJSON(t *testing.T) {
	c := &cmdClient{
		exec: func(_ context.Context, _ string, _ ...string) ([]byte, []byte, error) {
			return []byte(`[{"id":"m1","from":"a","to":"b","subject":"s"}]`), nil, nil
		},
	}
	got, err := c.CheckInbox(context.Background(), "p", "a")
	if err != nil {
		t.Fatalf("CheckInbox: %v", err)
	}
	if len(got) != 1 || got[0].ID != "m1" {
		t.Errorf("parsed = %+v", got)
	}
}
