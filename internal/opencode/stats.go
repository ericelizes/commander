package opencode

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// totalSpendRE picks up the line opencode prints summarizing total USD.
// opencode's text output is human-formatted ("Total: $1.23" / "Total spend: $1.23"),
// so we tolerate either phrasing and any whitespace. Best-effort only — if it
// doesn't match, TotalSpendUSD stays at zero and Raw is the contract.
var totalSpendRE = regexp.MustCompile(`(?i)total[^$\n]*\$\s*([0-9]+(?:\.[0-9]+)?)`)

func (c *execClient) Stats(ctx context.Context, project string, daysBack int) (*Stats, error) {
	args := []string{"stats"}
	if daysBack > 0 {
		args = append(args, "--days", strconv.Itoa(daysBack))
	}
	if project != "" {
		args = append(args, "--project", project)
	}

	cmd := c.cmd(ctx, args...)
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("opencode stats: %w (stderr: %s)", err, strings.TrimSpace(stderrBuf.String()))
	}

	raw := string(out)
	return &Stats{
		Raw:           raw,
		TotalSpendUSD: parseTotalSpend(raw),
		DaysBack:      daysBack,
		Project:       project,
	}, nil
}

func parseTotalSpend(raw string) float64 {
	m := totalSpendRE.FindStringSubmatch(raw)
	if len(m) < 2 {
		return 0
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	return v
}
