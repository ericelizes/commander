package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/store"
)

// rebalanceMove is one Manager `cmdr runtime rebalance` moves (or would).
type rebalanceMove struct {
	Role, From, To string
	Skipped        string // why it was not moved ("" = moved)
}

// rebalanceDeps are the seams of the rebalance: the live chats and the switch
// (cmdrd's POST /api/chat/<role>/runtime, which refuses a turn in flight).
type rebalanceDeps struct {
	home       string
	share      float64
	hooksReady bool
	list       func(ctx context.Context) ([]chat.Status, error)
	set        func(ctx context.Context, role, rt string) error
}

// liveManagers are the running Manager chats (never the Commander).
func liveManagers(chats []chat.Status) []chat.Status {
	var out []chat.Status
	for _, c := range chats {
		if c.Running && strings.HasPrefix(c.Role, "manager-") {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Role < out[j].Role })
	return out
}

func busy(c chat.Status) bool { return c.State == store.AgentWorking || len(c.Pending) > 0 }

// planRebalance finds how many Managers must move (need) so that round(share *
// live Managers) run on Codex, and the unpinned ones that may, in order:
// idle before mid-turn (marked skipped), then those whose workstream draws the
// target side (chat.CodexByShare), so a rebalance agrees with new Managers' draws.
func planRebalance(home string, managers []chat.Status, share float64, hooksReady bool) (target, onCodex, need int, cands []rebalanceMove) {
	target = int(math.Round(share * float64(len(managers))))
	for _, m := range managers {
		if m.Runtime == chat.RuntimeCodex {
			onCodex++
		}
	}
	from, to := chat.RuntimeClaude, chat.RuntimeCodex
	need = target - onCodex
	if need < 0 {
		from, to, need = chat.RuntimeCodex, chat.RuntimeClaude, -need
	}
	if need == 0 || to == chat.RuntimeCodex && !hooksReady {
		return target, onCodex, 0, nil
	}
	var ms []chat.Status
	for _, m := range managers {
		if (m.Runtime == from || from == chat.RuntimeClaude && m.Runtime == "") && !chat.RuntimePinned(home, m.Role) {
			ms = append(ms, m)
		}
	}
	rank := func(m chat.Status) int {
		r := 0
		if busy(m) {
			r += 2
		}
		if chat.CodexByShare(strings.TrimPrefix(m.Role, "manager-"), share) != (to == chat.RuntimeCodex) {
			r++
		}
		return r
	}
	sort.SliceStable(ms, func(i, j int) bool { return rank(ms[i]) < rank(ms[j]) })
	for _, m := range ms {
		mv := rebalanceMove{Role: m.Role, From: from, To: to}
		if busy(m) {
			mv.Skipped = "mid-turn"
		}
		cands = append(cands, mv)
	}
	return target, onCodex, need, cands
}

// rebalance plans and (unless dryRun) applies the moves. A move cmdrd refuses
// (the turn started meanwhile) is reported as skipped.
func rebalance(ctx context.Context, d rebalanceDeps, dryRun bool, out io.Writer) error {
	chats, err := d.list(ctx)
	if err != nil {
		return err
	}
	managers := liveManagers(chats)
	target, onCodex, need, cands := planRebalance(d.home, managers, d.share, d.hooksReady)
	fmt.Fprintf(out, "Managers on Codex: %d of %d live, target %d (codex_manager_share %.2f)\n", onCodex, len(managers), target, d.share)
	if target > onCodex && !d.hooksReady {
		fmt.Fprintln(out, "Codex runs no hooks (codex features list): no Manager is moved to Codex")
	}
	verb := "moved"
	if dryRun {
		verb = "would move"
	}
	moved := 0
	for _, mv := range cands {
		if moved == need {
			break
		}
		if mv.Skipped == "" && !dryRun {
			// cmdrd refuses a turn that started since the list (409): skip it.
			if err := d.set(ctx, mv.Role, mv.To); err != nil {
				mv.Skipped = err.Error()
			} else if err := chat.MarkRuntimeAuto(d.home, mv.Role); err != nil {
				return err
			}
		}
		if mv.Skipped != "" {
			fmt.Fprintf(out, "  skipped %s (%s -> %s): %s\n", mv.Role, mv.From, mv.To, mv.Skipped)
			continue
		}
		moved++
		fmt.Fprintf(out, "  %s %s: %s -> %s\n", verb, mv.Role, mv.From, mv.To)
	}
	if need == 0 {
		fmt.Fprintln(out, "  nothing to move")
	} else if moved < need {
		fmt.Fprintf(out, "  %d short of the target: the rest are pinned or mid-turn\n", need-moved)
	}
	return nil
}

// runRuntimeRebalance is `cmdr runtime rebalance [--dry-run]`.
func runRuntimeRebalance(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("runtime rebalance", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dryRun := fs.Bool("dry-run", false, "list the moves without making them")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	home := commanderHomeDir()
	c := cmdrdChats{home: home}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	d := rebalanceDeps{home: home, share: agentrt.CodexManagerShare(), hooksReady: chat.CodexHooksReady(), list: c.list,
		set: c.setRuntime}
	if err := rebalance(ctx, d, *dryRun, stdout); err != nil {
		fmt.Fprintf(stderr, "cmdr runtime rebalance: %v\n", err)
		return 1
	}
	return 0
}

// setRuntime asks cmdrd to move role to the selected runtime.
func (c cmdrdChats) setRuntime(ctx context.Context, role, rt string) error {
	return c.post(ctx, role, "runtime", map[string]any{"runtime": rt})
}

// list is cmdrd's GET /api/chat: every chat it knows.
func (c cmdrdChats) list(ctx context.Context) ([]chat.Status, error) {
	raw, err := os.ReadFile(filepath.Join(c.home, "api-url"))
	if err != nil {
		return nil, fmt.Errorf("cmdrd has no api-url: %w", err)
	}
	token, _ := os.ReadFile(filepath.Join(c.home, "api-token"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(strings.TrimSpace(string(raw)), "/")+"/api/chat", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Sessions []chat.Status `json:"sessions"`
		Error    string        `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cmdrd chat list: %s", body.Error)
	}
	return body.Sessions, nil
}
