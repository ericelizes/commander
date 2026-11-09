package notify

import (
	"bytes"
	"context"
	"errors"
	osexec "os/exec"
	"strings"
	"testing"
)

// fakeExec captures the most recent argv passed to system.exec. The returned
// *exec.Cmd points at the no-op /bin/true (or "true" via PATH) so
// CombinedOutput succeeds without actually firing the real osascript /
// notify-send. argv is captured from the call, not parsed from the *Cmd, so
// this works on platforms where /bin/true doesn't exist.
type fakeExec struct {
	name string
	args []string
}

func (f *fakeExec) capture(ctx context.Context, name string, args ...string) *osexec.Cmd {
	f.name = name
	f.args = args
	// "true" is in PATH on darwin, linux, and most CI images. CombinedOutput
	// returns "" + nil, which is exactly the "success" shape the production
	// path expects.
	return osexec.CommandContext(ctx, "true")
}

// TestNotify_DarwinArgv asserts the macOS path shells out to osascript with
// `-e <script>` containing the title and body literally — and that the
// AppleScript fragment matches the canonical
// `display notification "body" with title "title"` shape.
func TestNotify_DarwinArgv(t *testing.T) {
	fe := &fakeExec{}
	s := &system{runtimeOS: "darwin", exec: fe.capture}

	if err := s.Notify(context.Background(), "ceiling-hit", "operator-x killed"); err != nil {
		t.Fatalf("Notify(darwin): %v", err)
	}

	if fe.name != "osascript" {
		t.Fatalf("name: want osascript, got %q", fe.name)
	}
	if len(fe.args) != 2 || fe.args[0] != "-e" {
		t.Fatalf("args: want [-e <script>], got %v", fe.args)
	}
	want := `display notification "operator-x killed" with title "ceiling-hit"`
	if fe.args[1] != want {
		t.Fatalf("script: want %q, got %q", want, fe.args[1])
	}
}

// TestNotify_DarwinEscapesQuotesAndBackslashes makes sure user-supplied text
// containing the two AppleScript-breaking characters (`"` and `\`) round-trips
// as escaped sequences, not as raw script terminators / escape introducers.
func TestNotify_DarwinEscapesQuotesAndBackslashes(t *testing.T) {
	fe := &fakeExec{}
	s := &system{runtimeOS: "darwin", exec: fe.capture}

	title := `say "hi"`
	body := `path: C:\Users\<user> and "quoted"`
	if err := s.Notify(context.Background(), title, body); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if len(fe.args) != 2 {
		t.Fatalf("args len: %v", fe.args)
	}
	got := fe.args[1]

	// Backslashes doubled, double-quotes prefixed by a backslash.
	wantBody := `path: C:\\Users\\<user> and \"quoted\"`
	wantTitle := `say \"hi\"`
	if !strings.Contains(got, wantBody) {
		t.Errorf("body not escaped properly. got: %q", got)
	}
	if !strings.Contains(got, wantTitle) {
		t.Errorf("title not escaped properly. got: %q", got)
	}

	// And the script must still start with `display notification "` — i.e.
	// the escaping didn't break the surrounding template.
	if !strings.HasPrefix(got, `display notification "`) {
		t.Errorf("script malformed: %q", got)
	}
}

// TestNotify_LinuxArgv covers the happy Linux path: not WSL, notify-send is
// in PATH (we stub the WSL check via readFile so /proc/version is treated as
// "not WSL"). Asserts argv matches `notify-send <title> <body>`.
func TestNotify_LinuxArgv(t *testing.T) {
	fe := &fakeExec{}
	s := &system{
		runtimeOS: "linux",
		exec:      fe.capture,
		readFile: func(string) ([]byte, error) {
			return []byte("Linux version 6.1.0 generic"), nil
		},
	}

	// LookPath("notify-send") may legitimately fail in the test sandbox. To
	// make this test hermetic we only assert argv when notify-send is
	// installed; otherwise the production code took the console fallback
	// path and that's covered by other tests.
	if _, err := osexec.LookPath("notify-send"); err != nil {
		t.Skip("notify-send not installed; argv path covered when available")
	}

	if err := s.Notify(context.Background(), "ceiling-hit", "operator-x killed"); err != nil {
		t.Fatalf("Notify(linux): %v", err)
	}
	if fe.name != "notify-send" {
		t.Fatalf("name: want notify-send, got %q", fe.name)
	}
	if len(fe.args) != 2 || fe.args[0] != "ceiling-hit" || fe.args[1] != "operator-x killed" {
		t.Fatalf("args: want [ceiling-hit, operator-x killed], got %v", fe.args)
	}
}

// TestNotify_LinuxWSLFallsBackToConsole asserts that when /proc/version
// contains "Microsoft" we route to the console path instead of trying to
// shell out to notify-send (which would either be missing or fail to reach
// a graphical session).
func TestNotify_LinuxWSLFallsBackToConsole(t *testing.T) {
	fe := &fakeExec{}
	var buf bytes.Buffer
	prevOut := consoleOut
	consoleOut = &buf
	defer func() { consoleOut = prevOut }()

	s := &system{
		runtimeOS: "linux",
		exec:      fe.capture,
		readFile: func(string) ([]byte, error) {
			// The WSL2 kernel string actually used by Microsoft's images.
			return []byte("Linux version 5.15.90.1-microsoft-standard-WSL2"), nil
		},
	}

	if err := s.Notify(context.Background(), "title", "body"); err != nil {
		t.Fatalf("Notify(WSL): %v", err)
	}

	if fe.name != "" {
		t.Fatalf("WSL path should not shell out, but exec saw name=%q args=%v", fe.name, fe.args)
	}
	if got := buf.String(); !strings.Contains(got, "[NOTIFY] title: body") {
		t.Fatalf("console fallback: want [NOTIFY] line, got %q", got)
	}
}

// TestNotify_LinuxWSLCapitalizedAlsoDetected covers the older WSL1 kernel
// signature that uses capital "Microsoft". Both spellings must trigger the
// fallback.
func TestNotify_LinuxWSLCapitalizedAlsoDetected(t *testing.T) {
	fe := &fakeExec{}
	var buf bytes.Buffer
	prevOut := consoleOut
	consoleOut = &buf
	defer func() { consoleOut = prevOut }()

	s := &system{
		runtimeOS: "linux",
		exec:      fe.capture,
		readFile: func(string) ([]byte, error) {
			return []byte("Linux version 4.4.0-Microsoft (Microsoft@Microsoft.com)"), nil
		},
	}

	if err := s.Notify(context.Background(), "t", "b"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if fe.name != "" {
		t.Fatalf("expected console fallback, exec was called: %q %v", fe.name, fe.args)
	}
	if !strings.Contains(buf.String(), "[NOTIFY] t: b") {
		t.Fatalf("console fallback missing")
	}
}

// TestNotify_UnknownOSConsoleFallback covers the default branch: an
// unrecognized GOOS routes to the console path.
func TestNotify_UnknownOSConsoleFallback(t *testing.T) {
	var buf bytes.Buffer
	prevOut := consoleOut
	consoleOut = &buf
	defer func() { consoleOut = prevOut }()

	s := &system{runtimeOS: "plan9"}
	if err := s.Notify(context.Background(), "ping", "pong"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if !strings.Contains(buf.String(), "[NOTIFY] ping: pong") {
		t.Fatalf("console fallback: got %q", buf.String())
	}
}

// TestMockNotifier_RecordsCalls is the contract test for the test seam.
// Tests that depend on injecting a notifier into the daemon need this to
// actually capture call-by-call.
func TestMockNotifier_RecordsCalls(t *testing.T) {
	m := &MockNotifier{}
	if err := m.Notify(context.Background(), "a", "1"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if err := m.Notify(context.Background(), "b", "2"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	calls := m.Snapshot()
	if len(calls) != 2 {
		t.Fatalf("calls len: want 2, got %d", len(calls))
	}
	if calls[0] != (NotifyCall{Title: "a", Body: "1"}) {
		t.Errorf("call[0]: %+v", calls[0])
	}
	if calls[1] != (NotifyCall{Title: "b", Body: "2"}) {
		t.Errorf("call[1]: %+v", calls[1])
	}
}

// TestMockNotifier_PropagatesError covers the error-injection path used by
// tests that assert the daemon logs (and survives) a notifier failure.
func TestMockNotifier_PropagatesError(t *testing.T) {
	want := errors.New("boom")
	m := &MockNotifier{NotifyErr: want}
	if err := m.Notify(context.Background(), "x", "y"); !errors.Is(err, want) {
		t.Fatalf("err: want %v, got %v", want, err)
	}
	if len(m.Snapshot()) != 1 {
		t.Fatalf("call still recorded even on error path")
	}
}
