package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
)

// runtimeReportRow is one runtime's line of `cmdr runtime report`.
type runtimeReportRow struct {
	Runtime                                     string
	Started, Finished, Died, Respawned, Shipped int
	Forced                                      int           // started on Claude by a service keyword
	MedianFinish                                time.Duration // 0 = none finished
	Tokens                                      int64
}

// runtimeUnrecorded labels runs spawned before runtimes were recorded.
const runtimeUnrecorded = "unrecorded"

// buildRuntimeReport groups runs by the runtime recorded at their spawn and
// attributes each exited/died/shipped event to the agent's latest run spawned
// before it. A run is a respawn when an earlier run had the same agent and bead.
func buildRuntimeReport(runs []store.AgentRun, events []store.CostEvent, tokensOf func(store.AgentRun) int64) []runtimeReportRow {
	runtimeOf := map[int64]string{}
	byAgent := map[string][]store.AgentRun{} // spawn order
	for _, r := range runs {
		byAgent[r.AgentName] = append(byAgent[r.AgentName], r)
	}
	runAt := func(agent string, at int64) *store.AgentRun {
		var hit *store.AgentRun
		for i, r := range byAgent[agent] {
			if r.SpawnTime <= at {
				hit = &byAgent[agent][i]
			}
		}
		return hit
	}
	finishedAt := map[int64]int64{}
	died, shipped := map[int64]bool{}, map[int64]bool{}
	forced := map[int64]bool{}
	for _, e := range events {
		r := runAt(e.AgentName, e.OccurredAt)
		if r == nil {
			continue
		}
		switch e.EventType {
		case store.EventOperatorRuntime:
			if e.OccurredAt == r.SpawnTime {
				runtimeOf[r.ID] = e.Reason.String
				if e.Reason.String == spawn.ReasonClaudeServiceKeyword || e.Reason.String == spawn.ReasonClaudeHooksUnavailable || e.Reason.String == spawn.ReasonClaudeCodexOut {
					runtimeOf[r.ID], forced[r.ID] = chat.RuntimeClaude, true
				}
			}
		case store.EventOperatorExited:
			if _, ok := finishedAt[r.ID]; !ok {
				finishedAt[r.ID] = e.OccurredAt
			}
		case store.EventOperatorDied:
			died[r.ID] = true
		case store.EventOperatorShipped:
			shipped[r.ID] = true
		}
	}
	rows := map[string]*runtimeReportRow{}
	finish := map[string][]time.Duration{}
	seen := map[string]bool{} // agent + bead
	for _, r := range runs {
		rt := runtimeOf[r.ID]
		if rt == "" {
			rt = runtimeUnrecorded
		}
		row := rows[rt]
		if row == nil {
			row = &runtimeReportRow{Runtime: rt}
			rows[rt] = row
		}
		row.Started++
		if forced[r.ID] {
			row.Forced++
		}
		key := r.AgentName + "\x00" + r.TaskBeadID
		if seen[key] {
			row.Respawned++
		}
		seen[key] = true
		if at, ok := finishedAt[r.ID]; ok {
			row.Finished++
			finish[rt] = append(finish[rt], time.Duration(at-r.SpawnTime)*time.Second)
		}
		if died[r.ID] {
			row.Died++
		}
		if shipped[r.ID] {
			row.Shipped++
		}
		row.Tokens += tokensOf(r)
	}
	out := make([]runtimeReportRow, 0, len(rows))
	for rt, row := range rows {
		if d := finish[rt]; len(d) > 0 {
			sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
			row.MedianFinish = d[len(d)/2]
			if len(d)%2 == 0 {
				row.MedianFinish = (d[len(d)/2-1] + d[len(d)/2]) / 2
			}
		}
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Runtime < out[j].Runtime })
	return out
}

func writeRuntimeReport(w io.Writer, days int, rows []runtimeReportRow) {
	fmt.Fprintf(w, "Operators, last %d days\n", days)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "runtime\tstarted\tfinished\tdied\trespawned\tmedian finish\tshipped\ttokens\tforced to claude")
	for _, r := range rows {
		median := "-"
		if r.MedianFinish > 0 {
			median = strings.TrimSuffix(r.MedianFinish.Round(time.Minute).String(), "0s")
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%s\t%d\t%d\t%d\n", r.Runtime, r.Started, r.Finished, r.Died, r.Respawned, median, r.Shipped, r.Tokens, r.Forced)
	}
	_ = tw.Flush()
}

// split is how many of the live Managers and Operators run on Codex; managers
// < 0 when cmdrd (which knows the live chats) cannot be asked.
type split struct{ managersCodex, managers, operatorsCodex, operators int }

// liveSplit counts the running Manager chats (cmdrd) and the running host
// Operators (agent_runs, runtime from each role's runtime file).
func liveSplit(ctx context.Context, home string, st *store.Store) split {
	s := split{managers: -1}
	if chats, err := (cmdrdChats{home: home}).list(ctx); err == nil {
		s.managers = 0
		for _, c := range liveManagers(chats) {
			s.managers++
			if c.Runtime == chat.RuntimeCodex {
				s.managersCodex++
			}
		}
	}
	if runs, err := st.ListRunningRuns(ctx); err == nil {
		for _, r := range runs {
			if !strings.HasPrefix(r.AgentName, "operator-") {
				continue
			}
			s.operators++
			if chat.ReadRuntime(home, r.AgentName) == chat.RuntimeCodex {
				s.operatorsCodex++
			}
		}
	}
	return s
}

func writeShares(w io.Writer, s split) {
	fmt.Fprintf(w, "Shares on Codex: Managers %.2f (codex_manager_share), Operators %.2f (codex_operator_share)\n",
		agentrt.CodexManagerShare(), agentrt.CodexOperatorShare())
	managers := "unknown (cmdrd not running)"
	if s.managers >= 0 {
		managers = fmt.Sprintf("%d/%d", s.managersCodex, s.managers)
	}
	fmt.Fprintf(w, "Live on Codex: Managers %s, Operators %d/%d\n\n", managers, s.operatorsCodex, s.operators)
}

// runRuntimeReport is `cmdr runtime report [--days N]`.
func runRuntimeReport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("runtime report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	days := fs.Int("days", 7, "how many days back")
	if err := fs.Parse(args); err != nil || *days <= 0 {
		return 2
	}
	path, err := stateDBPath()
	if err != nil {
		fmt.Fprintf(stderr, "cmdr runtime report: %v\n", err)
		return 1
	}
	st, err := store.Open(path)
	if err != nil {
		fmt.Fprintf(stderr, "cmdr runtime report: %v\n", err)
		return 1
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	since := time.Now().Add(-time.Duration(*days) * 24 * time.Hour).Unix()
	runs, err := st.ListRunsSince(ctx, since)
	if err == nil {
		var events []store.CostEvent
		events, err = st.ListCostEventsSince(ctx, since, store.EventOperatorRuntime, store.EventOperatorExited,
			store.EventOperatorDied, store.EventOperatorShipped)
		if err == nil {
			writeShares(stdout, liveSplit(ctx, commanderHomeDir(), st))
			writeRuntimeReport(stdout, *days, buildRuntimeReport(runs, events, runTokens(ctx, st, runs)))
			return 0
		}
	}
	fmt.Fprintf(stderr, "cmdr runtime report: %v\n", err)
	return 1
}

// runTokens is a run's tokens: what the ceilings watchdog stored, else (for the
// agent's latest run, whose output file is still there) the tokens counted from
// its stream-json output the way the watchdog counts them.
func runTokens(ctx context.Context, st *store.Store, runs []store.AgentRun) func(store.AgentRun) int64 {
	latest := map[string]int64{}
	for _, r := range runs {
		latest[r.AgentName] = r.ID
	}
	paths := map[int64]string{}
	return func(r store.AgentRun) int64 {
		if r.TokensUsed > 0 || latest[r.AgentName] != r.ID {
			return r.TokensUsed
		}
		path, ok := paths[r.ProjectID]
		if !ok {
			if p, err := st.GetProjectByID(ctx, r.ProjectID); err == nil && p != nil {
				path = p.Path
			}
			paths[r.ProjectID] = path
		}
		if path == "" {
			return 0
		}
		return streamTokens(filepath.Join(path, ".commander", "operators", strings.TrimPrefix(r.AgentName, "operator-")+".fifo"))
	}
}

// streamTokens sums the tokens in an Operator's output file (Claude
// stream-json, which the Codex bridge also writes).
func streamTokens(path string) int64 {
	// Older builds left real fifos under the .fifo name: opening one blocks.
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return 0
	}
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	m := agentrt.NewClaudeEventMapper()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	var n int64
	for sc.Scan() {
		if ev, ok := m.Map(sc.Bytes()); ok && ev.Type == "step_finish" {
			n += int64(ev.Part.Tokens.Input + ev.Part.Tokens.Output)
		}
	}
	return n
}
