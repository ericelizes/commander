package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// statsPayload mirrors the (line-of-)JSON `docker stats --format json` emits.
//
// Fields are strings — docker formats CPUPerc as "0.12%" and MemUsage as
// "12.34MiB / 256MiB". We parse them in Stats below.
type statsPayload struct {
	CPUPerc  string `json:"CPUPerc"`
	MemUsage string `json:"MemUsage"`
}

// Stats runs `docker stats <id> --no-stream --format json` and parses
// CPUPercent + memory used/limit for the side panel.
//
// Returns an error (not nil-result) when the container is missing — Stats
// is only called for known-running containers, unlike Inspect which the
// reaper calls speculatively.
func (c *cmdClient) Stats(ctx context.Context, idOrName string) (*RuntimeStats, error) {
	if idOrName == "" {
		return nil, fmt.Errorf("docker: Stats requires id or name")
	}
	stdout, stderr, runErr := c.exec(ctx, "docker", "stats", idOrName, "--no-stream", "--format", "json")
	if runErr != nil {
		return nil, fmt.Errorf("docker stats: %w: %s", runErr, strings.TrimSpace(string(stderr)))
	}

	line := strings.TrimSpace(string(stdout))
	// docker stats with a single container emits a single JSON object
	// per line; if multiple lines come back, take the first.
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	if line == "" {
		return nil, fmt.Errorf("docker stats: empty output")
	}

	var p statsPayload
	if jerr := json.Unmarshal([]byte(line), &p); jerr != nil {
		return nil, fmt.Errorf("docker stats: parse: %w", jerr)
	}

	cpu, perr := parseCPUPercent(p.CPUPerc)
	if perr != nil {
		return nil, fmt.Errorf("docker stats: cpu: %w", perr)
	}
	used, limit, merr := parseMemUsage(p.MemUsage)
	if merr != nil {
		return nil, fmt.Errorf("docker stats: mem: %w", merr)
	}

	return &RuntimeStats{
		CPUPercent:  cpu,
		MemoryUsed:  used,
		MemoryLimit: limit,
	}, nil
}

// parseCPUPercent strips the trailing "%" from "0.12%" and parses the float.
func parseCPUPercent(s string) (float64, error) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	if s == "" {
		return 0, nil
	}
	return strconv.ParseFloat(s, 64)
}

// parseMemUsage parses "12.34MiB / 256MiB" into (used, limit) bytes.
//
// docker's separator is " / " (space-slash-space). Each side is a number
// followed by a binary unit (B, KiB, MiB, GiB, TiB). We accept the SI
// units (KB/MB/GB/TB) too because some daemons emit them.
func parseMemUsage(s string) (used, limit int64, err error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected '<used> / <limit>', got %q", s)
	}
	used, err = parseSize(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, err
	}
	limit, err = parseSize(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, err
	}
	return used, limit, nil
}

// parseSize parses "12.34MiB" / "256MB" / "0B" into bytes.
func parseSize(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	// Find the first non-numeric (non-digit, non-dot, non-sign) character.
	idx := -1
	for i, r := range s {
		if (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '+' {
			continue
		}
		idx = i
		break
	}
	if idx < 0 {
		// All digits — treat as bytes.
		n, perr := strconv.ParseFloat(s, 64)
		if perr != nil {
			return 0, perr
		}
		return int64(n), nil
	}
	numPart := s[:idx]
	unit := strings.TrimSpace(s[idx:])
	n, perr := strconv.ParseFloat(numPart, 64)
	if perr != nil {
		return 0, fmt.Errorf("size %q: %w", s, perr)
	}
	mult, ok := sizeUnit(unit)
	if !ok {
		return 0, fmt.Errorf("unknown size unit %q in %q", unit, s)
	}
	return int64(n * float64(mult)), nil
}

// sizeUnit maps docker's size suffix to a byte multiplier.
func sizeUnit(u string) (int64, bool) {
	switch strings.ToLower(u) {
	case "b", "":
		return 1, true
	case "kb":
		return 1000, true
	case "kib":
		return 1024, true
	case "mb":
		return 1000 * 1000, true
	case "mib":
		return 1024 * 1024, true
	case "gb":
		return 1000 * 1000 * 1000, true
	case "gib":
		return 1024 * 1024 * 1024, true
	case "tb":
		return 1000 * 1000 * 1000 * 1000, true
	case "tib":
		return 1024 * 1024 * 1024 * 1024, true
	}
	return 0, false
}
