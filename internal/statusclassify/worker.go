package statusclassify

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/store"
)

// Worker uses normal connected account allowances. The configurable token
// ceiling is local protection, not a claim about provider subscription billing.
type Worker struct {
	Store         *store.Store
	Engine        *chat.Engine
	Home          string
	Runner        Runner
	MonthlyTokens int64
	mu            sync.Mutex
	cooldown      map[string]time.Time
}

func (w *Worker) Run(ctx context.Context) {
	if w.Runner == nil {
		w.Runner = CLIRunner{Home: w.Home}
	}
	if w.MonthlyTokens <= 0 {
		w.MonthlyTokens = 500_000_000
		if n, e := strconv.ParseInt(os.Getenv("COMMANDER_STATUS_MONTHLY_TOKENS"), 10, 64); e == nil && n >= 0 {
			w.MonthlyTokens = n
		}
	}
	w.cooldown = map[string]time.Time{}
	watchDone := make(chan struct{})
	go func() { defer close(watchDone); w.watchChats(ctx) }()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); w.work(ctx) }()
	}
	wg.Wait()
	<-watchDone
}

func (w *Worker) watchChats(ctx context.Context) {
	active := map[string]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	defer wg.Wait()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		for _, status := range w.Engine.List() {
			if !status.Running {
				continue
			}
			role := status.Role
			lastSeq := status.LastSeq
			mu.Lock()
			running := active[role]
			if !running {
				active[role] = true
			}
			mu.Unlock()
			if running {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { mu.Lock(); delete(active, role); mu.Unlock() }()
				w.watchRole(ctx, role, lastSeq)
			}()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) watchRole(ctx context.Context, role string, lastSeq uint64) {
	var seq int64
	e := w.Store.DB().QueryRowContext(ctx, `SELECT source_seq FROM agent_sessions WHERE role=?`, role).Scan(&seq)
	if e != nil {
		seq = 0
		if lastSeq > 200 {
			seq = int64(lastSeq) - 200
		}
	}
	replay, sub, e := w.Engine.SubscribeBuffered(role, uint64(seq), 64)
	if e != nil {
		return
	}
	defer sub.Close()
	observe := func(ev chat.Event) bool {
		if ev.SessionID == "" || ev.Source == chat.SourceTranscript {
			return true
		}
		text := ""
		enqueue := false
		switch ev.Kind {
		case chat.KindSession:
			text = "Session started"
		case chat.KindUser:
			text = "New instruction: " + ev.Text
		case chat.KindText:
			text = "Agent message: " + ev.Text
			enqueue = true
		case chat.KindPermission, chat.KindQuestion, chat.KindPlan:
			raw, _ := json.Marshal(ev.Request)
			text = "Recorded runtime request: " + string(raw)
			enqueue = true
		case chat.KindToolResult:
			raw, _ := json.Marshal(ev.Tool)
			text = "Tool evidence: " + string(raw)
			enqueue = true
		case chat.KindResult:
			text = "Agent turn ended"
			enqueue = true
		default:
			return true
		}
		// Old history establishes the cursor/evidence without spending allowance.
		if time.Since(ev.Time) > 10*time.Minute {
			enqueue = false
		}
		writeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if e := w.Store.ObserveClassificationForRun(writeCtx, ev.Role, ev.RunID, ev.SessionID, int64(ev.Seq), text, enqueue); e != nil {
			log.Printf("status classifier: evidence persistence unavailable for %s", role)
			return false
		}
		return true
	}
	for _, ev := range replay {
		if !observe(ev) {
			return
		}
	}
	cleanup := time.NewTicker(5 * time.Second)
	defer cleanup.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-cleanup.C:
			if !w.Engine.Running(role) {
				return
			}
		case ev, ok := <-sub.C:
			if !ok {
				return
			}
			if !observe(ev) {
				return
			}
		}
	}
}

func (w *Worker) candidates() []chat.AccountView {
	accounts := chat.Accounts(w.Home, chat.ReadUsage(w.Home), time.Now())
	out := []chat.AccountView{}
	for _, runtime := range []string{chat.RuntimeCodex, chat.RuntimeClaude} {
		for _, a := range accounts {
			if a.Runtime != runtime || !a.LoggedIn || a.DuplicateOf != "" || (a.Usage != nil && a.Usage.Out(time.Now())) {
				continue
			}
			if runtime == chat.RuntimeCodex && !SubscriptionCodex(a.Dir) {
				continue
			}
			key := a.Runtime + ":" + a.Name
			w.mu.Lock()
			until := w.cooldown[key]
			w.mu.Unlock()
			if time.Now().Before(until) {
				continue
			}
			out = append(out, a)
		}
	}
	return out
}

func (w *Worker) work(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		job, e := w.Store.LeaseClassification(ctx)
		if e != nil {
			log.Printf("status classifier: job storage unavailable")
		} else if job != nil {
			w.process(ctx, *job)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) process(ctx context.Context, j store.ClassificationJob) {
	attempted := false
	for _, account := range w.candidates() {
		model := "gpt-6-luna"
		if account.Runtime == chat.RuntimeClaude {
			model = "haiku"
		}
		key := j.Key(account.Runtime + ":" + account.Name)
		allowed, e := w.Store.ReserveClassification(ctx, key, account.Runtime, account.Name, model, w.MonthlyTokens)
		if e != nil || !allowed {
			continue
		}
		attempted = true
		rctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		answer, e := w.Runner.Run(rctx, account, j.Evidence)
		cancel()
		outcome := "ok"
		if e != nil {
			outcome = "failed"
		}
		if e2 := w.Store.SettleClassificationUsage(ctx, key, answer.Input, answer.Output, answer.Cached, outcome); e2 != nil {
			log.Printf("status classifier: usage settlement unavailable; reservation retained")
		}
		if e == nil {
			input := answer.Input - answer.Cached
			if input < 0 {
				input = 0
			}
			_ = w.Store.InsertUsageTurn(ctx, store.UsageTurn{TurnKey: "classifier:" + key, Source: "classifier", Role: "status-classifier", Runtime: account.Runtime, Account: account.Name, Model: model, InputTokens: input, OutputTokens: answer.Output, CacheRead: answer.Cached, OccurredAt: time.Now().Unix()})
			if e = w.Store.CompleteClassification(ctx, j, answer.Kind, strings.TrimSpace(answer.Description)); e != nil {
				log.Printf("status classifier: result persistence unavailable")
			}
			return
		}
		w.mu.Lock()
		w.cooldown[account.Runtime+":"+account.Name] = time.Now().Add(5 * time.Minute)
		w.mu.Unlock()
		log.Printf("status classifier: %s account unavailable; trying another connected small-model account", account.Runtime)
	}
	if !attempted {
		_, _ = w.Store.DB().ExecContext(ctx, `UPDATE classification_jobs SET state='pending',lease_until=0,attempts=GREATEST(attempts-1,0),updated_at=? WHERE role=? AND session_id=? AND source_seq=?`, time.Now().Add(time.Minute).Unix(), j.Role, j.SessionID, j.SourceSeq)
		return
	}
	if e := w.Store.CompleteClassification(ctx, j, "", ""); e != nil {
		log.Printf("status classifier: retry persistence unavailable: %s", fmt.Sprintf("%T", e))
	}
}
