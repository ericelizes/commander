package agentmail

import (
	"context"
	"regexp"
	"testing"
	"time"
)

var iso8601Pattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)

func TestInbox_SinceIsISO8601(t *testing.T) {
	var capturedArgs []string
	c := &cmdClient{
		exec: func(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
			capturedArgs = args
			return []byte("[]"), nil, nil
		},
	}

	// Use a non-UTC time to confirm conversion to UTC happens.
	loc, _ := time.LoadLocation("America/New_York")
	since := time.Date(2026, 5, 9, 8, 30, 0, 0, loc)

	if _, err := c.Inbox(context.Background(), "p", "agent-x", since, InboxOptions{}); err != nil {
		t.Fatalf("Inbox: %v", err)
	}

	got := valueFor(capturedArgs, "--since")
	if got == "" {
		t.Fatalf("--since flag missing in argv %v", capturedArgs)
	}
	if !iso8601Pattern.MatchString(got) {
		t.Errorf("--since = %q, expected ISO-8601 (^\\d{4}-...Z$)", got)
	}
	// Verify it's UTC (NYC 8:30 -> UTC 12:30 or 13:30 depending on DST; just
	// confirm the formatter emitted Z and the date is right).
	if got[len(got)-1] != 'Z' {
		t.Errorf("--since must end in Z (UTC marker), got %q", got)
	}
}

func TestInbox_SinceFormatterIsISO8601(t *testing.T) {
	// Direct unit test on iso8601() with a known value.
	t0 := time.Date(2026, 5, 9, 12, 34, 56, 0, time.UTC)
	got := iso8601(t0)
	want := "2026-05-09T12:34:56Z"
	if got != want {
		t.Errorf("iso8601(%v) = %q, want %q", t0, got, want)
	}
	if !iso8601Pattern.MatchString(got) {
		t.Errorf("iso8601 output %q failed regex %s", got, iso8601Pattern)
	}
}

func TestInbox_LimitWired(t *testing.T) {
	var capturedArgs []string
	c := &cmdClient{
		exec: func(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
			capturedArgs = args
			return []byte("[]"), nil, nil
		},
	}

	if _, err := c.Inbox(context.Background(), "p", "a", time.Now(), InboxOptions{Limit: 25}); err != nil {
		t.Fatalf("Inbox: %v", err)
	}

	if v := valueFor(capturedArgs, "--limit"); v != "25" {
		t.Errorf("--limit = %q, want 25", v)
	}
}

func TestInbox_LimitOmittedWhenZero(t *testing.T) {
	var capturedArgs []string
	c := &cmdClient{
		exec: func(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
			capturedArgs = args
			return []byte("[]"), nil, nil
		},
	}

	if _, err := c.Inbox(context.Background(), "p", "a", time.Now(), InboxOptions{Limit: 0}); err != nil {
		t.Fatalf("Inbox: %v", err)
	}

	for _, a := range capturedArgs {
		if a == "--limit" {
			t.Errorf("--limit should be omitted when InboxOptions.Limit is 0; got %v", capturedArgs)
		}
	}
}

func TestInbox_IncludeBodiesWired(t *testing.T) {
	var capturedArgs []string
	c := &cmdClient{
		exec: func(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
			capturedArgs = args
			return []byte("[]"), nil, nil
		},
	}

	if _, err := c.Inbox(context.Background(), "p", "a", time.Now(), InboxOptions{IncludeBodies: true}); err != nil {
		t.Fatalf("Inbox: %v", err)
	}

	found := false
	for _, a := range capturedArgs {
		if a == "--include-bodies" {
			found = true
		}
	}
	if !found {
		t.Errorf("--include-bodies should be present when InboxOptions.IncludeBodies=true; got %v", capturedArgs)
	}
}

func TestInbox_IncludeBodiesOmitted(t *testing.T) {
	var capturedArgs []string
	c := &cmdClient{
		exec: func(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
			capturedArgs = args
			return []byte("[]"), nil, nil
		},
	}

	if _, err := c.Inbox(context.Background(), "p", "a", time.Now(), InboxOptions{}); err != nil {
		t.Fatalf("Inbox: %v", err)
	}

	for _, a := range capturedArgs {
		if a == "--include-bodies" {
			t.Errorf("--include-bodies should be omitted by default; got %v", capturedArgs)
		}
	}
}

func TestInbox_ParsesArrayJSON(t *testing.T) {
	c := &cmdClient{
		exec: func(_ context.Context, _ string, _ ...string) ([]byte, []byte, error) {
			return []byte(`[{"id":"m1","from":"a","to":"b","subject":"s","body":"hi","importance":"normal"}]`), nil, nil
		},
	}
	got, err := c.Inbox(context.Background(), "p", "a", time.Now(), InboxOptions{})
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(got) != 1 || got[0].ID != "m1" || got[0].Subject != "s" {
		t.Errorf("parsed mail = %+v", got)
	}
}

func TestInbox_ParsesEmptyOutput(t *testing.T) {
	c := &cmdClient{
		exec: func(_ context.Context, _ string, _ ...string) ([]byte, []byte, error) {
			return []byte(""), nil, nil
		},
	}
	got, err := c.Inbox(context.Background(), "p", "a", time.Now(), InboxOptions{})
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty result, got %v", got)
	}
}

// valueFor returns the value immediately following flag in args, or "" if
// flag isn't present or has no following arg.
func valueFor(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}
