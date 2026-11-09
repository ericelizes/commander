package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// inspectFormat is the Go-template fed to `docker inspect --format` so we
// get exactly the fields commander cares about as a single JSON object.
//
// .Created is RFC3339Nano; we parse with time.RFC3339Nano below.
const inspectFormat = `{"id":"{{.Id}}","name":"{{.Name}}","image":"{{.Config.Image}}","status":"{{.State.Status}}","createdAt":"{{.Created}}","labels":{{json .Config.Labels}}}`

type inspectPayload struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Image     string            `json:"image"`
	Status    string            `json:"status"`
	CreatedAt string            `json:"createdAt"`
	Labels    map[string]string `json:"labels"`
}

// Inspect runs `docker inspect <id> --format <go-template-json>` and
// projects the result into ContainerInfo. Returns (nil, nil) when the
// container does not exist (NOT an error) so the orphan reaper can race
// containers exiting without log spam.
func (c *cmdClient) Inspect(ctx context.Context, idOrName string) (*ContainerInfo, error) {
	if idOrName == "" {
		return nil, fmt.Errorf("docker: Inspect requires id or name")
	}
	stdout, stderr, runErr := c.exec(ctx, "docker", "inspect", idOrName, "--format", inspectFormat)
	if runErr != nil {
		if isNoSuchContainer(stderr) || isNoSuchObject(stderr) {
			return nil, nil
		}
		return nil, fmt.Errorf("docker inspect: %w: %s", runErr, strings.TrimSpace(string(stderr)))
	}

	line := strings.TrimSpace(string(stdout))
	if line == "" {
		return nil, nil
	}

	var p inspectPayload
	if jerr := json.Unmarshal([]byte(line), &p); jerr != nil {
		return nil, fmt.Errorf("docker inspect: parse: %w", jerr)
	}

	info := &ContainerInfo{
		ID:     p.ID,
		Name:   strings.TrimPrefix(p.Name, "/"),
		Image:  p.Image,
		Status: p.Status,
		Labels: p.Labels,
	}
	if p.CreatedAt != "" {
		// docker emits RFC3339Nano; fall back to RFC3339 if the daemon
		// happens to omit fractional seconds.
		if t, terr := time.Parse(time.RFC3339Nano, p.CreatedAt); terr == nil {
			info.CreatedAt = t
		} else if t, terr := time.Parse(time.RFC3339, p.CreatedAt); terr == nil {
			info.CreatedAt = t
		}
	}
	return info, nil
}

// isNoSuchObject matches docker inspect's specific not-found phrasing
// ("No such object: <id>") which differs from `docker stop`'s wording.
func isNoSuchObject(stderr []byte) bool {
	return strings.Contains(string(stderr), "No such object")
}
