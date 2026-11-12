package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// placeGateCovers is the place path's gate rule, with no warn-only mode: a
// closed user gate covers the step (an approved gate whose run reaches it on
// the board, or the closed first gate of its workflow scope). An open gate
// above it refuses as for any spawn.
func placeGateCovers(ctx context.Context, bc beads.Client, dir, step string) error {
	if g := coveringApproval(step); g != "" {
		return nil
	}
	all, err := readOnceMore(ctx, func() ([]beads.Bead, error) {
		return bc.List(ctx, dir, beads.ListOptions{Status: "all"})
	})
	if err != nil {
		return gateReadError(step, err)
	}
	byID := make(map[string]beads.Bead, len(all))
	for _, b := range all {
		byID[b.ID] = b
	}
	scope := workflowGateScope(beads.Lineage(ctx, bc, dir, nil, step), byID)
	if scope == "" {
		return fmt.Errorf("a place run needs a closed user gate over %s, and it has no workflow scope above it", step)
	}
	var gateIDs []string
	for _, b := range all {
		if isGateBead(b) && !isRetiredBead(b) && isFirstGateCandidate(b) {
			gateIDs = append(gateIDs, b.ID)
		}
	}
	parents, err := readOnceMore(ctx, func() (map[string]string, error) {
		return parentsOf(ctx, bc, dir, gateIDs)
	})
	if err != nil {
		return gateReadError(step, err)
	}
	if closed, _ := firstGateOf(scope, all, parents); closed {
		return nil
	}
	return fmt.Errorf("a place run needs a closed user gate over %s: %s", step, workflowGateMessage(scope, ""))
}

// placeSpawnRefusal judges the flags of a spawn on a place before anything is
// written. These are policy refusals: no retry, no Commander mail.
func placeSpawnRefusal(stepPlace, flagPlace, runtime, account string, writesWorkflow bool) error {
	if flagPlace != "" && flagPlace != stepPlace {
		if stepPlace == "" {
			return fmt.Errorf("--place %s: the step has no run-on label; label it first (cmdr internal-set-objective --run-on %s)", flagPlace, flagPlace)
		}
		return fmt.Errorf("--place %s does not match the step's label run-on:%s", flagPlace, stepPlace)
	}
	if stepPlace == "" {
		return nil
	}
	if runtime != "" && runtime != "claude" {
		return fmt.Errorf("a place run is Claude only (--runtime %s refused): Codex has no mail hook there", runtime)
	}
	if account != "" {
		return fmt.Errorf("a place run uses the Mac's own Claude login (--account refused)")
	}
	if writesWorkflow {
		return fmt.Errorf("a place run needs an approved workflow (--writes-workflow refused)")
	}
	return nil
}

// pokePlaceOffer asks cmdrd to send a recorded offer to its place now; when
// cmdrd is down or the Mac is not connected, the Mac gets it at its next hello.
func pokePlaceOffer(ctx context.Context, home string, runID int64) (sent bool) {
	raw, err := os.ReadFile(filepath.Join(home, "api-url"))
	if err != nil {
		return false
	}
	token, _ := os.ReadFile(filepath.Join(home, "api-token"))
	body, _ := json.Marshal(map[string]any{"run_id": runID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(strings.TrimSpace(string(raw)), "/")+"/api/places/offer", bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var out struct {
		Sent bool `json:"sent"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.Sent
}

// beadLabels is a bead's labels, nil for nil.
func beadLabels(b *beads.Bead) []string {
	if b == nil {
		return nil
	}
	return b.Labels
}

// placeRunLookup is the store reads stopPlaceRuns needs.
type placeRunLookup interface {
	GetAgentRunByAgentName(ctx context.Context, name string) (*store.AgentRun, error)
	GetPlaceRun(ctx context.Context, runID int64) (*store.PlaceRun, error)
}

// postPlaceStop asks cmdrd to stop a place run; tests swap it.
var postPlaceStop = func(ctx context.Context, home string, runID int64, by string) error {
	raw, err := os.ReadFile(filepath.Join(home, "api-url"))
	if err != nil {
		return fmt.Errorf("cmdrd has no api-url: %w", err)
	}
	token, _ := os.ReadFile(filepath.Join(home, "api-token"))
	body, _ := json.Marshal(map[string]any{"run_id": runID, "by": by})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(strings.TrimSpace(string(raw)), "/")+"/api/places/stop", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cmdrd answered %d", resp.StatusCode)
	}
	return nil
}

// stopPlaceRuns ends the place runs among roles (internal-mail --interrupt): a
// stop only takes power away, so the server may send it; the Mac ends the
// process group. The mail sent with it stays unread. It returns the stopped
// roles and the roles that are not place runs.
func stopPlaceRuns(ctx context.Context, home string, st any, roles []string, from string, stderr io.Writer) (stopped, rest []string) {
	stopped, rest = []string{}, []string{}
	look, ok := st.(placeRunLookup)
	if !ok {
		return stopped, roles
	}
	for _, role := range roles {
		var run *store.AgentRun
		if strings.HasPrefix(role, "operator-") {
			run, _ = look.GetAgentRunByAgentName(ctx, role)
		}
		var pr *store.PlaceRun
		if run != nil && run.Status == "running" {
			pr, _ = look.GetPlaceRun(ctx, run.ID)
		}
		if pr == nil || !store.PlaceRunActive(pr.State) {
			rest = append(rest, role)
			continue
		}
		err := error(nil)
		if callerIsOperator(from) {
			err = fmt.Errorf("an Operator cannot stop another Operator")
		} else {
			err = postPlaceStop(ctx, home, run.ID, from)
		}
		if err != nil {
			fmt.Fprintf(stderr, "warning: %s on %s NOT stopped: %v\n", run.AgentName, pr.Place, err)
			continue
		}
		stopped = append(stopped, run.AgentName)
		fmt.Fprintf(stderr, "note: %s on %s stopped: its run token ended and the Mac ends its process; it will not read this mail. cmdrd hands the step back within a minute.\n", run.AgentName, pr.Place)
	}
	return stopped, rest
}
