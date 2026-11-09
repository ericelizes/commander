// Package tokenledger reads every agent's log into the usage_turns table, so
// token use can be broken down by account, agent and model: the native chats'
// logs (Commander and Managers, COMMANDER_HOME/chat/<role>.jsonl) and each
// host Operator's stream log (<project>/.commander/operators/<flat>.fifo).
//
// A chat turn reports its own usage. An Operator's result lines carry totals
// for the run so far, so its turns are the difference from the previous one.
package tokenledger

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/example-user/commander/internal/store"
)

// Store is the slice of *store.Store the ledger needs.
type Store interface {
	InsertUsageTurn(ctx context.Context, u store.UsageTurn) error
	UsageScanState(ctx context.Context, path string) (int64, string, error)
	SetUsageScanState(ctx context.Context, path string, offset int64, state string) error
	SetLatestRunUsage(ctx context.Context, agentName, model string, tokens int64, usd float64) error
}

// Ledger scans the logs. Account names the Claude account a role runs on.
type Ledger struct {
	Store    Store
	Home     string                                      // COMMANDER_HOME
	Projects func(ctx context.Context) ([]string, error) // project paths
	Account  func(role string) string
}

// Scan reads what each log gained since the last scan.
func (l Ledger) Scan(ctx context.Context) error {
	chats, _ := filepath.Glob(filepath.Join(l.Home, "chat", "*.jsonl"))
	for _, path := range chats {
		role := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		if err := l.scanFile(ctx, path, &chatReader{l: l, role: role, path: path}); err != nil {
			return err
		}
	}
	if l.Projects == nil {
		return nil
	}
	projects, err := l.Projects(ctx)
	if err != nil {
		return err
	}
	for _, p := range projects {
		logs, _ := filepath.Glob(filepath.Join(p, ".commander", "operators", "*.fifo"))
		for _, path := range logs {
			// Older runs piped into a real FIFO: opening one blocks, and it holds no history.
			if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
				continue
			}
			role := "operator-" + strings.TrimSuffix(filepath.Base(path), ".fifo")
			if err := l.scanFile(ctx, path, &operatorReader{l: l, role: role, path: path}); err != nil {
				return err
			}
		}
	}
	return nil
}

// reader turns one log's lines into turns; its state rides between scans.
type reader interface {
	line(ctx context.Context, b []byte, offset int64, mtime time.Time) error
	state() any
	done(ctx context.Context) error
}

// carried is what a reader keeps between scans.
type carried struct {
	Model  string            `json:"model,omitempty"`
	Prev   map[string]totals `json:"prev,omitempty"`   // an Operator run's last cumulative totals
	Tokens int64             `json:"tokens,omitempty"` // an Operator log's totals so far
	USD    float64           `json:"usd,omitempty"`
}

type totals struct {
	In, Out, CacheRead, CacheCreate int64
	USD                             float64
}

func (t totals) sum() int64 { return t.In + t.Out + t.CacheRead + t.CacheCreate }

// minus is t since prev; a total that went down started over (a new process).
func (t totals) minus(prev totals) totals {
	if t.In < prev.In || t.Out < prev.Out || t.CacheRead < prev.CacheRead || t.CacheCreate < prev.CacheCreate || t.USD < prev.USD-1e-9 {
		return t
	}
	return totals{t.In - prev.In, t.Out - prev.Out, t.CacheRead - prev.CacheRead, t.CacheCreate - prev.CacheCreate, t.USD - prev.USD}
}

func (l Ledger) scanFile(ctx context.Context, path string, r reader) error {
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	offset, raw, err := l.Store.UsageScanState(ctx, path)
	if err != nil {
		return err
	}
	if fi.Size() < offset { // rewritten: read it again from the top
		offset, raw = 0, ""
	}
	if fi.Size() == offset {
		return nil
	}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), r.state())
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	br := bufio.NewReaderSize(f, 1<<20)
	pos := offset
	for {
		b, err := br.ReadBytes('\n')
		if len(b) > 0 && b[len(b)-1] == '\n' { // only whole lines: the writer may be mid-line
			if lerr := r.line(ctx, b, pos, fi.ModTime()); lerr != nil {
				return lerr
			}
			pos += int64(len(b))
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("tokenledger: read %s: %w", path, err)
		}
	}
	if err := r.done(ctx); err != nil {
		return err
	}
	state, _ := json.Marshal(r.state())
	return l.Store.SetUsageScanState(ctx, path, pos, string(state))
}

// usage is a turn's usage as Claude and the bridge write it.
type usage struct {
	In          int64 `json:"input_tokens"`
	Out         int64 `json:"output_tokens"`
	CacheRead   int64 `json:"cache_read_input_tokens"`
	CacheCreate int64 `json:"cache_creation_input_tokens"`
}

// modelUsage sums Claude's per-model cumulative totals; model is the one model
// it names ("" when several or none).
func modelUsage(raw json.RawMessage) (t totals, model string, ok bool) {
	var m map[string]struct {
		In          int64   `json:"inputTokens"`
		Out         int64   `json:"outputTokens"`
		CacheRead   int64   `json:"cacheReadInputTokens"`
		CacheCreate int64   `json:"cacheCreationInputTokens"`
		USD         float64 `json:"costUSD"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil || len(m) == 0 {
		return totals{}, "", false
	}
	for name, u := range m {
		t.In += u.In
		t.Out += u.Out
		t.CacheRead += u.CacheRead
		t.CacheCreate += u.CacheCreate
		t.USD += u.USD
		model = name
	}
	if len(m) > 1 {
		model = ""
	}
	return t, model, true
}

// runtimeModel is the runtime and plain model name for a reported model:
// "codex:gpt-6.1-sol" runs on codex.
func runtimeModel(model string) (runtime, name string) {
	if rest, ok := strings.CutPrefix(model, "codex:"); ok {
		return "codex", rest
	}
	return "claude", model
}

// chatReader reads a native chat's log (internal/chat event lines).
type chatReader struct {
	l     Ledger
	role  string
	path  string
	carry carried
}

func (c *chatReader) state() any                 { return &c.carry }
func (c *chatReader) done(context.Context) error { return nil }

var (
	sessionMark = []byte(`"kind":"session"`)
	resultMark  = []byte(`"kind":"turn_result"`)
)

func (c *chatReader) line(ctx context.Context, b []byte, offset int64, _ time.Time) error {
	isSession, isResult := bytes.Contains(b, sessionMark), bytes.Contains(b, resultMark)
	if !isSession && !isResult {
		return nil
	}
	var ev struct {
		Seq     int64     `json:"seq"`
		Time    time.Time `json:"time"`
		Session *struct {
			Model string `json:"model"`
		} `json:"session"`
		Result *struct {
			DeltaUSD   float64         `json:"cost_delta_usd"`
			Usage      json.RawMessage `json:"usage"`
			ModelUsage json.RawMessage `json:"model_usage"`
		} `json:"result"`
	}
	if json.Unmarshal(b, &ev) != nil {
		return nil
	}
	if isSession && ev.Session != nil && ev.Session.Model != "" {
		c.carry.Model = ev.Session.Model
		return nil
	}
	if ev.Result == nil {
		return nil
	}
	runtime, model := runtimeModel(c.carry.Model)
	// usage is this turn's own; model_usage is cumulative, and a resumed
	// process restarts it from an old total, so it only names the model.
	if _, named, ok := modelUsage(ev.Result.ModelUsage); ok && named != "" {
		model = named
	}
	var u usage
	_ = json.Unmarshal(ev.Result.Usage, &u)
	t := totals{u.In, u.Out, u.CacheRead, u.CacheCreate, ev.Result.DeltaUSD}
	if t.sum() == 0 && t.USD == 0 {
		return nil
	}
	key := c.role + ":" + strconv.FormatInt(ev.Seq, 10)
	if ev.Seq == 0 {
		key = c.path + ":" + strconv.FormatInt(offset, 10)
	}
	at := ev.Time
	if at.IsZero() {
		at = time.Now()
	}
	return c.l.Store.InsertUsageTurn(ctx, store.UsageTurn{
		TurnKey: key, Source: "chat", Role: c.role, Runtime: runtime, Account: c.l.account(runtime, c.role), Model: model,
		InputTokens: t.In, OutputTokens: t.Out, CacheRead: t.CacheRead, CacheCreate: t.CacheCreate, USD: t.USD, OccurredAt: at.Unix(),
	})
}

// operatorReader reads an Operator's stream log (Claude stream-json, which
// the Codex bridge also writes).
type operatorReader struct {
	l       Ledger
	role    string
	path    string
	carry   carried
	changed bool
}

func (o *operatorReader) state() any { return &o.carry }

func (o *operatorReader) line(ctx context.Context, b []byte, offset int64, mtime time.Time) error {
	isInit := bytes.Contains(b, []byte(`"subtype":"init"`))
	isResult := bytes.Contains(b, []byte(`"type":"result"`))
	if !isInit && !isResult {
		return nil
	}
	var ev struct {
		Type       string          `json:"type"`
		Subtype    string          `json:"subtype"`
		Model      string          `json:"model"`
		TotalUSD   float64         `json:"total_cost_usd"`
		Usage      json.RawMessage `json:"usage"`
		ModelUsage json.RawMessage `json:"modelUsage"`
	}
	if json.Unmarshal(b, &ev) != nil {
		return nil
	}
	if ev.Type == "system" && ev.Subtype == "init" {
		if ev.Model != "" {
			o.carry.Model = ev.Model
		}
		return nil
	}
	if ev.Type != "result" {
		return nil
	}
	runtime, model := runtimeModel(o.carry.Model)
	var t totals
	if cum, named, ok := modelUsage(ev.ModelUsage); ok {
		cum.USD = ev.TotalUSD
		if o.carry.Prev == nil {
			o.carry.Prev = map[string]totals{}
		}
		t = cum.minus(o.carry.Prev["run"])
		o.carry.Prev["run"] = cum
		if named != "" {
			model = named
		}
	} else {
		var u usage
		_ = json.Unmarshal(ev.Usage, &u)
		t = totals{u.In, u.Out, u.CacheRead, u.CacheCreate, 0}
	}
	if t.sum() == 0 && t.USD == 0 {
		return nil
	}
	o.carry.Tokens += t.sum()
	o.carry.USD += t.USD
	o.changed = true
	return o.l.Store.InsertUsageTurn(ctx, store.UsageTurn{
		TurnKey: o.path + ":" + strconv.FormatInt(offset, 10), Source: "operator", Role: o.role, Runtime: runtime,
		Account: o.l.account(runtime, o.role), Model: model,
		InputTokens: t.In, OutputTokens: t.Out, CacheRead: t.CacheRead, CacheCreate: t.CacheCreate, USD: t.USD, OccurredAt: mtime.Unix(),
	})
}

// done puts the Operator's model and totals on its newest run.
func (o *operatorReader) done(ctx context.Context) error {
	if !o.changed {
		return nil
	}
	_, model := runtimeModel(o.carry.Model)
	return o.l.Store.SetLatestRunUsage(ctx, o.role, model, o.carry.Tokens, o.carry.USD)
}

// account is the account a turn counts against: codex, or the role's Claude account.
func (l Ledger) account(runtime, role string) string {
	if runtime == "codex" {
		return "codex"
	}
	if l.Account != nil {
		if a := l.Account(role); a != "" {
			return a
		}
	}
	return "default"
}
