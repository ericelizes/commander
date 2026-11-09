package notify

import (
	"fmt"
	"io"
	"os"
)

// consoleOut is where the console fallback writes. Stderr by choice: stdout
// is reserved for whatever the host process treats as program output (the
// daemon currently uses it for nothing user-facing, but launchd/systemd
// route stderr to a log file by default, which is exactly where these
// fallback notifications belong).
var consoleOut io.Writer = os.Stderr

// notifyConsole is the universal fallback: write a single line to stderr in
// the canonical "[NOTIFY] <title>: <body>" shape. Used by every backend that
// can't reach a graphical surface (unknown GOOS, WSL, missing notify-send),
// so its format is intentionally stable for grep / log scraping.
func (s *system) notifyConsole(title, body string) error {
	_, err := fmt.Fprintf(consoleOut, "[NOTIFY] %s: %s\n", title, body)
	return err
}

// defaultReadFile is a tiny indirection so linux.go can default to os.ReadFile
// without importing os in two places. Kept here (not in linux.go) so all
// "default I/O" lives next to the consoleOut sink.
func defaultReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
