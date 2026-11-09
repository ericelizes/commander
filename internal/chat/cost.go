package chat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/example-user/commander/internal/store"
)

// CostRecord is one finished turn of a role, as handed to the CostSink.
type CostRecord struct {
	Role         string
	SessionID    string
	TotalUSD     float64 // cumulative session cost after the turn
	DeltaUSD     float64
	InputTokens  int64
	OutputTokens int64
	CacheRead    int64
	CacheCreate  int64
	At           time.Time
}

// CostSink stores per-turn costs. It must be idempotent on (SessionID,
// TotalUSD): a replayed or backfilled turn is the same turn.
type CostSink interface {
	RecordChatCost(ctx context.Context, r CostRecord) error
}

// costRecord builds the record for a result event; ok is false for results
// that cost nothing and carry no session (nothing to count).
func costRecord(ev Event) (CostRecord, bool) {
	if ev.Kind != KindResult || ev.Result == nil || ev.Source == SourceTranscript || ev.SessionID == "" {
		return CostRecord{}, false
	}
	r := CostRecord{Role: ev.Role, SessionID: ev.SessionID, TotalUSD: ev.Result.TotalCostUSD, DeltaUSD: ev.Result.CostDeltaUSD, At: ev.Time}
	var u struct {
		Input       int64 `json:"input_tokens"`
		Output      int64 `json:"output_tokens"`
		CacheRead   int64 `json:"cache_read_input_tokens"`
		CacheCreate int64 `json:"cache_creation_input_tokens"`
	}
	_ = json.Unmarshal(ev.Result.Usage, &u)
	r.InputTokens, r.OutputTokens, r.CacheRead, r.CacheCreate = u.Input, u.Output, u.CacheRead, u.CacheCreate
	return r, r.TotalUSD > 0
}

// BackfillCosts records the turns already in the chat logs (read-only), once
// per cmdrd start. Safe to repeat: the sink ignores turns it has.
func (e *Engine) BackfillCosts(ctx context.Context) (int, error) {
	if e.opts.Costs == nil {
		return 0, nil
	}
	files, _ := filepath.Glob(filepath.Join(e.logDir(), "*.jsonl"))
	n := 0
	for _, path := range files {
		got, err := backfillLog(ctx, path, e.opts.Costs)
		n += got
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func backfillLog(ctx context.Context, path string, sink CostSink) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	role := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	marker := []byte(`"turn_result"`)
	r := bufio.NewReaderSize(f, 1<<20)
	n := 0
	for {
		line, err := r.ReadBytes('\n')
		if bytes.Contains(line, marker) {
			var ev Event
			if json.Unmarshal(line, &ev) == nil {
				ev.Role = role
				if rec, ok := costRecord(ev); ok {
					if serr := sink.RecordChatCost(ctx, rec); serr != nil {
						return n, serr
					}
					n++
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
	}
}

// StoreCosts is the CostSink over the state db.
type StoreCosts struct{ Store *store.Store }

func (c StoreCosts) RecordChatCost(ctx context.Context, r CostRecord) error {
	_, err := c.Store.InsertChatCost(ctx, store.ChatCost{
		Role: r.Role, SessionID: r.SessionID, TotalUSD: r.TotalUSD, DeltaUSD: r.DeltaUSD,
		InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, CacheRead: r.CacheRead, CacheCreate: r.CacheCreate,
		OccurredAt: r.At.Unix(),
	})
	return err
}
