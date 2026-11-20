package api

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/example-user/commander/internal/brief"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/claudeacct"
	"github.com/example-user/commander/internal/config"
	"github.com/example-user/commander/internal/livedata"
)

// briefModel is the Brief's writer: an agent that investigates, four runs a day.
const briefModel = "sonnet"

// serveBrief is the Brief page: GET reads the latest, POST starts a rewrite
// (one at a time) and answers with the latest and generating:true.
func (s *Server) serveBrief(w http.ResponseWriter, r *http.Request) {
	if s.brief == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the Brief is not available"})
		return
	}
	if r.Method == http.MethodPost {
		s.brief.Start()
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.brief.Status())
}

// RunBrief rewrites the Brief on its schedule until ctx ends (cmdrd runs it, app open or not).
func (s *Server) RunBrief(ctx context.Context) {
	if s.brief != nil {
		s.brief.Run(ctx)
	}
}

// gatherBrief reads the Brief's sources: the board, the beads and final
// reports in Postgres, and the user's Linear tickets.
func (s *Server) gatherBrief(ctx context.Context, w brief.Windows) (brief.Sources, error) {
	snap := s.current(ctx)
	if snap.Degraded != nil || snap.Version == 0 {
		return brief.Sources{}, errors.New("live state unavailable")
	}
	if s.opts.StateStore == nil || !s.opts.StateStore.IsPostgres() {
		return brief.Sources{}, errors.New("the Brief reads beads from Postgres")
	}
	src := brief.Sources{Tree: livedata.BuildTree(snap, time.Now().Unix()), WaitUntil: waitUntils(snap), PRs: stepPRs(snap)}
	var err error
	if src.Beads, src.Reports, err = s.opts.StateStore.BriefBeads(ctx, w.Since()); err != nil {
		return brief.Sources{}, err
	}
	src.Linear, src.LinearNote = brief.ReadLinear(ctx, filepath.Dir(s.opts.ConfigPath), w.Since(), nil, "")
	return src, nil
}

// waitUntils is each step's earliest time wait, by bead id.
func waitUntils(snap livedata.Snapshot) map[string]int64 {
	out := map[string]int64{}
	var walk func([]livedata.SnapStep)
	walk = func(steps []livedata.SnapStep) {
		for _, st := range steps {
			if st.WaitUntil > 0 {
				out[st.BeadID] = st.WaitUntil
			}
			walk(st.Children)
		}
	}
	for _, ws := range snap.Workstreams {
		walk(ws.Steps)
	}
	return out
}

// stepPRs is each step's pull requests as owner/repo#n, by bead id.
func stepPRs(snap livedata.Snapshot) map[string][]string {
	out := map[string][]string{}
	for _, ws := range snap.Workstreams {
		var walk func([]livedata.SnapStep)
		walk = func(steps []livedata.SnapStep) {
			for _, st := range steps {
				repo := st.PRRepo
				if repo == "" {
					repo = ws.Repo
				}
				for _, ref := range st.PR {
					out[st.BeadID] = append(out[st.BeadID], repo+"#"+strings.TrimPrefix(ref, "#"))
				}
				walk(st.Children)
			}
		}
		walk(ws.Steps)
	}
	return out
}

// briefGenerator is the Brief's reader on the Commander's Claude account, as the user.
func briefGenerator(opts Options) brief.Generate {
	home := filepath.Dir(opts.ConfigPath)
	if opts.Chat != nil {
		home = opts.Chat.Home()
	}
	prompt := brief.AgentPrompt
	if cfg, err := config.Load(opts.ConfigPath); err == nil {
		if name := strings.TrimSpace(config.UserName(cfg)); name != "" {
			prompt += "\nThe person is " + name + ": write as them, in the first person (I, my); never their name."
		}
	}
	return func(ctx context.Context, in brief.Input) (string, error) {
		return brief.Agent(briefModel, prompt, claudeacct.Env(home, chat.ReadAccount(home, "commander")))(ctx, in)
	}
}
