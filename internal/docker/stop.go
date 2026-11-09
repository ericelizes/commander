package docker

import (
	"context"
	"fmt"
	"strings"
)

// Stop runs `docker stop <id>`.
//
// Idempotent: a "No such container" stderr (the docker daemon's response
// when the container has already exited and been --rm'd) is treated as
// success. Commander's cleanup paths blindly stop on shutdown; this
// keeps them quiet.
func (c *cmdClient) Stop(ctx context.Context, idOrName string) error {
	if idOrName == "" {
		return fmt.Errorf("docker: Stop requires id or name")
	}
	_, stderr, err := c.exec(ctx, "docker", "stop", idOrName)
	if err != nil {
		if isNoSuchContainer(stderr) {
			return nil
		}
		return fmt.Errorf("docker stop: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// isNoSuchContainer matches the docker daemon's error string for a missing
// container. Substring match keeps us tolerant of message drift between
// docker engine releases.
func isNoSuchContainer(stderr []byte) bool {
	s := string(stderr)
	return strings.Contains(s, "No such container") ||
		strings.Contains(s, "is not running")
}
