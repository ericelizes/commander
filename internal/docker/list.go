package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// listPayload mirrors the line-of-JSON `docker ps --format json` emits,
// with just the fields the orphan reaper needs.
//
// docker ps emits one JSON object per line (NOT a JSON array). Labels come
// across as a comma-separated string ("k=v,k2=v2"), which we parse below.
type listPayload struct {
	ID         string `json:"ID"`
	Names      string `json:"Names"`
	Image      string `json:"Image"`
	Status     string `json:"Status"`
	State      string `json:"State"`
	Labels     string `json:"Labels"`
	CreatedAt  string `json:"CreatedAt"`
	RunningFor string `json:"RunningFor"`
}

// ListByLabel runs `docker ps --filter label=<labelKey> --format json` and
// returns every container matching that label.
//
// Returns an empty slice (NEVER nil) on no matches so callers can blindly
// range without a nil check. Used by the orphan reaper:
//
//	containers, _ := client.ListByLabel(ctx, "commander.project")
//	for _, c := range containers {
//	    // reconcile against agent_runs by container_id
//	}
func (c *cmdClient) ListByLabel(ctx context.Context, labelKey string) ([]ContainerInfo, error) {
	if labelKey == "" {
		return nil, fmt.Errorf("docker: ListByLabel requires labelKey")
	}
	stdout, stderr, runErr := c.exec(ctx, "docker", "ps", "--filter", "label="+labelKey, "--format", "json")
	if runErr != nil {
		return nil, fmt.Errorf("docker ps: %w: %s", runErr, strings.TrimSpace(string(stderr)))
	}

	out := make([]ContainerInfo, 0)
	for _, raw := range strings.Split(string(stdout), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		var p listPayload
		if jerr := json.Unmarshal([]byte(line), &p); jerr != nil {
			return nil, fmt.Errorf("docker ps: parse line %q: %w", line, jerr)
		}
		info := ContainerInfo{
			ID:     p.ID,
			Name:   p.Names,
			Image:  p.Image,
			Status: stateOrStatus(p),
			Labels: parseLabelString(p.Labels),
		}
		// docker ps's CreatedAt format varies ("2026-05-09 10:00:00 -0700 PDT");
		// best-effort parse, leave zero on failure.
		if t, terr := time.Parse("2006-01-02 15:04:05 -0700 MST", p.CreatedAt); terr == nil {
			info.CreatedAt = t
		}
		out = append(out, info)
	}
	return out, nil
}

// stateOrStatus picks the more-useful field for ContainerInfo.Status. docker ps
// gives both a high-level State ("running") and a detailed Status ("Up 2 minutes")
// — Inspect returns just State, so prefer that for parity, falling back to Status.
func stateOrStatus(p listPayload) string {
	if p.State != "" {
		return p.State
	}
	return p.Status
}

// parseLabelString turns "k=v,k2=v2" (docker ps's Labels field) into a map.
// Returns an empty map (not nil) so callers don't trip on a nil Labels field
// when the container has no labels.
func parseLabelString(s string) map[string]string {
	out := make(map[string]string)
	if s == "" {
		return out
	}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		eq := strings.IndexByte(pair, '=')
		if eq < 0 {
			out[pair] = ""
			continue
		}
		out[pair[:eq]] = pair[eq+1:]
	}
	return out
}
