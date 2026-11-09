//go:build !linux

package reviewrun

import "context"

// Other Unix hosts own the launched process group. Detached reviewer commands
// are unsupported there; this is not equivalent to Linux cgroup containment.
func reviewIsolation(_ context.Context, program string, args []string) (string, []string, func() error, error) {
	return program, args, func() error { return nil }, nil
}
