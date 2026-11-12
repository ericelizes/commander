package daemon

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/livedata"
)

// prURL is a GitHub pull request link: owner/repo and number.
var prURL = regexp.MustCompile(`https://github\.com/([\w.-]+/[\w.-]+)/pull/(\d+)`)

// prCreated is the pull request a `gh pr create` printed, read from the
// command and its output; ok is false for any other command.
func prCreated(input, output string) (repo string, n int, ok bool) {
	if !strings.Contains(input, "gh pr create") {
		return "", 0, false
	}
	m := prURL.FindAllStringSubmatch(output, -1)
	if len(m) == 0 {
		return "", 0, false
	}
	last := m[len(m)-1] // gh prints the new pull request's URL last
	n, err := strconv.Atoi(last[2])
	if err != nil {
		return "", 0, false
	}
	return last[1], n, true
}

// prLabel is a pull request's step label: pr:N in the project's own repo,
// pr:<owner/repo>#N anywhere else.
func prLabel(projectRepo, repo string, n int) string {
	if strings.EqualFold(projectRepo, repo) {
		return fmt.Sprintf("pr:%d", n)
	}
	return fmt.Sprintf("pr:%s#%d", repo, n)
}

// runPRLinker attaches every pull request an execution agent creates to the
// step it works on, so the board, the Overview and its Scope read it.
func (d *Daemon) runPRLinker(ctx context.Context) {
	var mu sync.Mutex
	active := map[string]bool{}
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		for _, status := range d.chat.ListRunning() {
			role := status.Role
			if !status.Running || !strings.HasPrefix(role, "operator-") {
				continue
			}
			mu.Lock()
			watching := active[role]
			active[role] = true
			mu.Unlock()
			if watching {
				continue
			}
			go func(role string, since uint64) {
				defer func() { mu.Lock(); delete(active, role); mu.Unlock() }()
				d.watchPRs(ctx, role, since)
			}(role, status.LastSeq)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (d *Daemon) watchPRs(ctx context.Context, role string, since uint64) {
	replay, sub, err := d.chat.SubscribeBuffered(role, since, 64)
	if err != nil {
		return
	}
	defer sub.Close()
	inputs := map[string]string{} // tool call id -> its input, while it runs
	handle := func(ev chat.Event) {
		if ev.Tool == nil || ev.RunID == 0 {
			return
		}
		switch ev.Kind {
		case chat.KindToolInput:
			if strings.Contains(string(ev.Tool.Input), "gh pr create") {
				inputs[ev.Tool.ID] = string(ev.Tool.Input)
			}
		case chat.KindToolResult:
			input, ok := inputs[ev.Tool.ID]
			if !ok {
				return
			}
			delete(inputs, ev.Tool.ID)
			if repo, n, ok := prCreated(input, ev.Tool.Content+"\n"+string(ev.Tool.Structured)); ok {
				d.linkPR(ctx, ev.RunID, repo, n)
			}
		}
	}
	for _, ev := range replay {
		handle(ev)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.C:
			if !ok {
				return
			}
			handle(ev)
		}
	}
}

func (d *Daemon) linkPR(ctx context.Context, runID int64, repo string, n int) {
	run, err := d.store.GetAgentRun(ctx, runID)
	if err != nil || run == nil || run.TaskBeadID == "" {
		return
	}
	p, err := d.store.GetProject(ctx, run.ProjectID)
	if err != nil || p == nil {
		return
	}
	label := prLabel(livedata.GitHubRepo(p.Path), repo, n)
	if err := beads.New().SetLabel(ctx, p.Path, run.TaskBeadID, label, true); err != nil {
		log.Printf("prlink: could not attach %s to %s: %v", label, run.TaskBeadID, err)
		return
	}
	log.Printf("prlink: attached %s to %s", label, run.TaskBeadID)
}
