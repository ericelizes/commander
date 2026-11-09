package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
)

// commanderRefreshInterval drives the periodic cross-fleet reload of
// projects, objectives, operators, and metrics. 5s matches the U12 plan's
// "auto-refresh every 5s" cadence — long enough to avoid hammering SQLite
// across every activated project, short enough that activating a 2nd
// project shows up before the user wonders whether anything happened.
const commanderRefreshInterval = 5 * time.Second

// The data layer lives in internal/livedata (shared with the cmdrd API); the
// TUI renders it under its historical names.
type (
	CommanderLiveData = livedata.Data
	ObjectiveRow      = livedata.ObjectiveRow
	FleetMetricsLive  = livedata.FleetMetrics
	CommanderDataDeps = livedata.Deps
)

// LoadCommanderData loads the cross-fleet snapshot, scraping the chat panes
// for agent state when running inside a cockpit pane.
func LoadCommanderData(ctx context.Context, deps CommanderDataDeps) CommanderLiveData {
	// One shared reader: cmdrd polls br/state.db once for every column. Fall back
	// to loading locally only when the daemon cannot be reached.
	if d, ok := daemonLiveData(ctx); ok {
		return d
	}
	return LoadCommanderDataLocal(ctx, deps)
}

// LoadCommanderDataLocal reads br and state.db itself, bypassing cmdrd's
// snapshot, which lags a bead created moments ago.
func LoadCommanderDataLocal(ctx context.Context, deps CommanderDataDeps) CommanderLiveData {
	if deps.Scrape == nil {
		deps.Scrape = scrapeAgentStates
	}
	return livedata.Load(ctx, deps)
}

// clearFinishedOperatorOwners: see livedata.ClearFinishedOperatorOwners.
func clearFinishedOperatorOwners(ctx context.Context, st *store.Store, projects []store.Project, rows []ObjectiveRow) []ObjectiveRow {
	return livedata.ClearFinishedOperatorOwners(ctx, st, projects, rows)
}

func reviewPR(labels []string) string { return livedata.ReviewPR(labels) }

// scrapeAgentStates reads the chat panes only from inside a cockpit pane
// (not in tests or plain shells).
func scrapeAgentStates(now time.Time) map[string]store.AgentState {
	if os.Getenv("TMUX") == "" {
		return nil
	}
	return livedata.ScrapeAgentStates(now)
}

func classifyPane(text string) (state, detail string) { return livedata.ClassifyPane(text) }

func mergeAgentStates(hooks, scraped map[string]store.AgentState, now time.Time) map[string]store.AgentState {
	return livedata.MergeAgentStates(hooks, scraped, now)
}

// daemonLiveData fetches the loaded Data from cmdrd's /api/livedata. It is
// skipped with COMMANDER_NO_DAEMON=1 (tests get a temp COMMANDER_HOME with no
// api-url) and returns ok=false on any problem so the caller loads locally.
// Not gated on tmux: the native chats run cmdr outside it, and a local load
// is a full br read of every project.
func daemonLiveData(ctx context.Context) (CommanderLiveData, bool) {
	if os.Getenv("COMMANDER_NO_DAEMON") != "" {
		return CommanderLiveData{}, false
	}
	home := os.Getenv("COMMANDER_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return CommanderLiveData{}, false
		}
		home = filepath.Join(h, ".commander")
	}
	urlRaw, err1 := os.ReadFile(filepath.Join(home, "api-url"))
	tok, err2 := os.ReadFile(filepath.Join(home, "api-token"))
	if err1 != nil || err2 != nil {
		return CommanderLiveData{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(string(urlRaw))+"/api/livedata", nil)
	if err != nil {
		return CommanderLiveData{}, false
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return CommanderLiveData{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return CommanderLiveData{}, false
	}
	var w livedata.Wire
	if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
		return CommanderLiveData{}, false
	}
	d := livedata.FromWire(w)
	if d.LoadErr != nil && len(d.Projects) == 0 {
		return CommanderLiveData{}, false // the daemon's own read failed: try locally
	}
	return d, true
}
