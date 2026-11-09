package livedata

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/example-user/commander/internal/store"
)

// StaleWorking is how long a "working" agent may stay silent before it is shown
// idle: a Stop hook that never arrived (crash, killed pane) must not spin forever.
const StaleWorking = 10 * time.Minute

// execCommand is the tmux seam for the pane scrape.
var execCommand = exec.Command

// Claude's footer shows "esc to interrupt" only while a turn is running. It is
// the fallback signal for agents whose hooks are not installed yet (chats that
// started before the hooks existed): the hooks are exact, this is good enough.
const busyMarker = "esc to interrupt"

var scrapeSince = struct {
	sync.Mutex
	state map[string]store.AgentState
}{state: map[string]store.AgentState{}}

var waitingRe = regexp.MustCompile(`Waiting for (\d+) background agents?`)

// ClassifyPane reads Claude's footer from the bottom up; only the most recent
// status line counts. "esc to interrupt" is a running turn; "✻ Waiting for N
// background agent" is NOT idle (the Manager is blocked on sub-agents); a
// "✻ Cooked for 2m" style line is a finished turn.
func ClassifyPane(text string) (state, detail string) {
	state, detail = classifyStatusLine(text)
	if state == store.AgentIdle {
		// Between turns, but Claude's background-agent panel still lists running
		// sub-agents ("◯ general-purpose  Running … 22m"): not idle.
		if n := runningSubAgents(text); n > 0 {
			return store.AgentWaiting, subAgentsLabel(n)
		}
	}
	return state, detail
}

var subAgentRowRe = regexp.MustCompile(`^\s*[◯○]\s+\S.*\d+\s?[smhd]\b`)

// runningSubAgents counts the "◯ <type> <task> … <elapsed>" rows of the
// background-agent panel near the bottom of the pane.
func runningSubAgents(text string) int {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	n := 0
	for _, l := range lines {
		if subAgentRowRe.MatchString(l) {
			n++
		}
	}
	return n
}

func subAgentsLabel(n int) string {
	if n == 1 {
		return "1 sub-agent"
	}
	return fmt.Sprintf("%d sub-agents", n)
}

func classifyStatusLine(text string) (state, detail string) {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		switch {
		case strings.Contains(l, busyMarker):
			return store.AgentWorking, ""
		case waitingRe.MatchString(l):
			n, _ := strconv.Atoi(waitingRe.FindStringSubmatch(l)[1])
			return store.AgentWaiting, subAgentsLabel(n)
		case strings.HasPrefix(l, "✻") && strings.Contains(l, " for "):
			return store.AgentIdle, ""
		}
	}
	return store.AgentIdle, ""
}

// ScrapeAgentStates reads every chat pane of the "commander" tmux session
// tagged with a workstream and reports its state from what is on screen. It
// is read-only and best-effort: any tmux error gives no entries.
func ScrapeAgentStates(now time.Time) map[string]store.AgentState {
	out, err := execCommand("tmux", "list-panes", "-s", "-t", "commander", "-F",
		"#{pane_id}|#{@cmdr_chat}|#{@cmdr_ws}|#{window_name}").Output()
	if err != nil {
		return nil
	}
	states := map[string]store.AgentState{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.SplitN(line, "|", 4) // not a tab: tmux prints tabs as "_" outside a tty
		if len(f) < 4 || f[1] != "1" {
			continue
		}
		var role string
		switch {
		case f[2] != "":
			role = store.WorkstreamManagerRole(f[2])
		case f[3] == "commander":
			role = "commander" // window 0's chat, keyed by its window, not by a title
		default:
			continue
		}
		text, err := execCommand("tmux", "capture-pane", "-p", "-t", f[0], "-S", "-30").Output()
		if err != nil {
			continue
		}
		state, detail := ClassifyPane(string(text))
		states[role] = trackSince(role, state, detail, now)
	}
	return states
}

// trackSince keeps when each scraped agent last changed state, in this process.
func trackSince(role, state, detail string, now time.Time) store.AgentState {
	scrapeSince.Lock()
	defer scrapeSince.Unlock()
	prev, ok := scrapeSince.state[role]
	if !ok || prev.State != state {
		prev = store.AgentState{Role: role, State: state, Since: now.Unix()}
	}
	prev.Detail = detail
	prev.Updated = now.Unix()
	scrapeSince.state[role] = prev
	return prev
}

// MergeAgentStates prefers hook rows (exact, with the tool detail) that are
// fresh, and fills the rest from the screen scrape.
func MergeAgentStates(hooks, scraped map[string]store.AgentState, now time.Time) map[string]store.AgentState {
	out := make(map[string]store.AgentState, len(scraped)+len(hooks))
	for role, a := range scraped {
		out[role] = a
	}
	for role, h := range hooks {
		if sc, scrapedToo := scraped[role]; scrapedToo {
			if now.Unix()-h.Updated > int64(StaleWorking.Seconds()) {
				continue // the hook went quiet; trust the screen
			}
			if h.State == store.AgentIdle && sc.State == store.AgentWaiting {
				continue // Stop fires when a turn ends, even with sub-agents still running
			}
		}
		out[role] = h
	}
	return out
}

// EffectiveState is the state to show for an agent right now: a working or
// waiting agent that has gone silent past StaleWorking is shown idle.
func EffectiveState(a store.AgentState, now time.Time) string {
	switch a.State {
	case store.AgentWorking, store.AgentWaiting:
		if now.Unix()-a.Updated > int64(StaleWorking.Seconds()) {
			return store.AgentIdle
		}
	}
	return a.State
}
