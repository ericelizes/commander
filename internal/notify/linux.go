package notify

import (
	"context"
	"fmt"
	osexec "os/exec"
	"strings"
)

// procVersionPath is the file we read to sniff for WSL. Captured as a var so
// the test can repoint it; the readFile seam on system also lets us stub the
// content directly.
var procVersionPath = "/proc/version"

// notifyLinux drives the libnotify notification daemon via notify-send. Two
// fall-throughs to the console fallback:
//
//  1. We're running under WSL, where there's no graphical session for the
//     desktop notification daemon to render into. (WSLg exists, but we don't
//     want to assume it; the console line is always safe.)
//  2. notify-send isn't installed. Many minimal Linux installs (servers,
//     containers) don't ship libnotify. We don't want to crash the daemon
//     on a missing binary — fall back to the console line.
func (s *system) notifyLinux(ctx context.Context, title, body string) error {
	if s.isWSL() {
		return s.notifyConsole(title, body)
	}
	if _, err := osexec.LookPath("notify-send"); err != nil {
		return s.notifyConsole(title, body)
	}
	cmd := s.exec(ctx, "notify-send", title, body)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("notify(linux): notify-send: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// isWSL reads /proc/version and looks for the "Microsoft" / "microsoft"
// signature WSL kernels embed. This is the canonical detection technique
// recommended in Microsoft's own docs and what tools like wslu use. A read
// error is treated as "not WSL" — on a real Linux desktop /proc/version is
// always present, so the only way to read-fail is on a non-Linux GOOS, which
// can't reach this code path anyway.
func (s *system) isWSL() bool {
	rf := s.readFile
	if rf == nil {
		rf = defaultReadFile
	}
	data, err := rf(procVersionPath)
	if err != nil {
		return false
	}
	v := string(data)
	return strings.Contains(v, "Microsoft") || strings.Contains(v, "microsoft")
}
