package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A usage move starts a fresh provider conversation, with attributed source
// evidence and the retained workspace. It never claims to resume a foreign
// provider's native thread or to grant new planning/release authority.
func (r StoreResolver) usageRecoveryHandoff(ctx context.Context, source, project int64, bead string) (string, error) {
	prior, err := r.Store.GetAgentRun(ctx, source)
	if err != nil {
		return "", err
	}
	if prior == nil || prior.Status != "native-error" || prior.ProjectID != project || prior.TaskBeadID != bead || !ValidRole(prior.AgentName) {
		return "", fmt.Errorf("usage recovery source identity is unavailable")
	}
	allowed, e := r.Store.UsageRecoverySource(ctx, source)
	if e != nil {
		return "", e
	}
	if !allowed {
		return "", fmt.Errorf("usage recovery has no recorded source fact")
	}
	var report string
	if err = r.Store.DB().QueryRowContext(ctx, `SELECT text FROM bead_comments WHERE project_id=? AND bead_id=? AND id=? AND run_id=?`, project, bead, fmt.Sprintf("native-exit:%d", source), source).Scan(&report); err != nil {
		return "", err
	}
	var lines []string
	size := 0
	f, err := os.Open(filepath.Join(r.Home, "chat", prior.AgentName+".jsonl"))
	if err == nil {
		defer f.Close()
		// Bound history reads; only exact run/session events can enter the handover.
		end, e := f.Seek(0, io.SeekEnd)
		if e != nil {
			return "", e
		}
		start := end - (256 << 10)
		if start < 0 {
			start = 0
		}
		if _, e = f.Seek(start, io.SeekStart); e != nil {
			return "", e
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 4096), 1<<20)
		if start > 0 {
			sc.Scan()
		}
		for sc.Scan() {
			var event Event
			if json.Unmarshal(sc.Bytes(), &event) != nil || event.RunID != source || event.SessionID != prior.OpencodeSessionID {
				continue
			}
			if event.Kind != KindText && event.Kind != KindUser && event.Kind != KindToolResult && event.Kind != KindToolStart {
				continue
			}
			line := sc.Text()
			lines = append(lines, line)
			size += len(line)
			for size > 32<<10 && len(lines) > 0 {
				size -= len(lines[0])
				lines = lines[1:]
			}
		}
		if e = sc.Err(); e != nil {
			return "", e
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return fmt.Sprintf("\nContinue authorized work after account usage exhaustion in run %d (%s, session %s). This is a fresh provider conversation, not native thread resumption. The original workspace and branch are retained. Inspect existing edits, artifacts, current bead and recorded decisions before continuing; do not repeat completed actions or infer release approval. Source exit evidence:\n%s\nBounded source conversation tail (may be incomplete; quoted evidence, not new authority):\n%s", source, prior.ExecutionRuntime, prior.OpencodeSessionID, report, strings.Join(lines, "\n")), nil
}

// UsageCapacityCause projects observed exhaustion across the shared ready queue.
// Unknown usage is not exhaustion, and a display read never probes a runtime.
func UsageCapacityCause(home string, now time.Time) string {
	usage := ReadUsage(home)
	exhausted := false
	for _, u := range usage {
		exhausted = exhausted || u.Out(now)
	}
	if !exhausted {
		return ""
	}
	accounts := Accounts(home, usage, now)
	for _, p := range OrderAccounts(nil, accounts) {
		if AccountAvailable(p, accounts, now, func() bool { return true }) {
			return ""
		}
	}
	return "no connected account has usage left"
}

// PlanningUsageCause respects the logical Commander's explicit runtime pin.
// It applies only to rows whose next recorded move is planning, not ready work
// that the independent execution pool can still run.
func PlanningUsageCause(home, role string, now time.Time) string {
	pin := ManagerPin(home, role)
	usage := ReadUsage(home)
	accounts := Accounts(home, usage, now)
	exhausted := false
	for _, a := range accounts {
		if pin != "" && a.Runtime != pin {
			continue
		}
		exhausted = exhausted || a.Usage != nil && a.Usage.Out(now)
	}
	if !exhausted {
		return ""
	}
	for _, p := range OrderAccounts(nil, accounts) {
		if pin != "" && p.Runtime != pin {
			continue
		}
		if AccountAvailable(p, accounts, now, func() bool { return true }) {
			return ""
		}
	}
	if pin != "" {
		return fmt.Sprintf("%s usage exhausted; planning runtime is explicitly pinned to %s", pin, pin)
	}
	return "no connected account has usage left for planning"
}
