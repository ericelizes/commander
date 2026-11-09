package chat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Window is one plan usage window: the fraction used (0-1) and its reset.
type Window struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    int64   `json:"resets_at,omitempty"` // unix seconds
}

// Usage is the latest plan usage one account reported, from whichever chat on
// it saw a rate-limit update last. Claude reports it in stream-json
// rate_limit_event; Codex in app-server account/rateLimits/updated, which the
// bridge forwards in the same shape. Nothing polls Anthropic or OpenAI.
type Usage struct {
	Runtime  string  `json:"runtime"`           // claude or codex
	Account  string  `json:"account,omitempty"` // the Claude account ("" for codex)
	Status   string  `json:"status"`            // ok, warning or out
	FiveHour *Window `json:"five_hour,omitempty"`
	SevenDay *Window `json:"seven_day,omitempty"`
	// OutUntil is when an "out" account's limit resets (unix seconds; 0 = unknown).
	OutUntil int64     `json:"out_until,omitempty"`
	Role     string    `json:"role"` // the chat whose report this is
	At       time.Time `json:"at"`
}

// Usage statuses.
const (
	UsageOK      = "ok"
	UsageWarning = "warning"
	UsageOut     = "out"
)

// UsageKey names an account's usage: "claude:<account>" or "codex".
func UsageKey(runtime, account string) string {
	if runtime == RuntimeCodex {
		return RuntimeCodex
	}
	return RuntimeClaude + ":" + accountName(account)
}

// UsageFile is where cmdrd keeps the latest usage per account.
func UsageFile(home string) string { return filepath.Join(home, "chat", "usage.json") }

// Current is u as of now: an "out" whose reset passed is ok again, and a
// window whose reset passed starts over at 0.
func (u Usage) Current(now time.Time) Usage {
	passed := func(at int64) bool { return at > 0 && now.Unix() >= at }
	if u.Status == UsageOut && passed(u.OutUntil) {
		u.Status, u.OutUntil = UsageOK, 0
	}
	for _, w := range []**Window{&u.FiveHour, &u.SevenDay} {
		if *w != nil && passed((*w).ResetsAt) {
			*w = &Window{}
		}
	}
	return u
}

// Out reports whether u says the account has no usage left now.
func (u Usage) Out(now time.Time) bool { return u.Current(now).Status == UsageOut }

// UsageStore is the latest Usage per key, saved to UsageFile on every change.
type UsageStore struct {
	mu   sync.Mutex
	path string
	m    map[string]Usage
}

// OpenUsage loads home's usage file (missing = empty).
func OpenUsage(home string) *UsageStore {
	return &UsageStore{path: UsageFile(home), m: ReadUsage(home)}
}

// ReadUsage reads home's usage file without a store (the CLI with cmdrd down).
func ReadUsage(home string) map[string]Usage {
	m := map[string]Usage{}
	if raw, err := os.ReadFile(UsageFile(home)); err == nil {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}

// rateLimitInfo is Claude's rate_limit_info (and the bridge's Codex copy of it).
type rateLimitInfo struct {
	Status      string  `json:"status"` // allowed, allowed_warning, rejected
	ResetsAt    int64   `json:"resetsAt"`
	Type        string  `json:"rateLimitType"`
	Utilization float64 `json:"utilization"`
	Windows     struct {
		FiveHour *struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    int64   `json:"resetsAt"`
		} `json:"five_hour"`
		SevenDay *struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    int64   `json:"resetsAt"`
		} `json:"seven_day"`
	} `json:"unifiedWindows"`
	OverageStatus string `json:"overageStatus"`
	UsingOverage  bool   `json:"isUsingOverage"`
}

// Record merges one rate-limit report into key's usage. Reports are sparse
// (Claude names one window, Codex sends only what changed): windows a report
// leaves out keep their last value.
func (s *UsageStore) Record(runtime, account, role string, raw json.RawMessage, at time.Time) {
	var rl rateLimitInfo
	if json.Unmarshal(raw, &rl) != nil {
		return
	}
	key := UsageKey(runtime, account)
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.m[key]
	u.Runtime, u.Role, u.At = runtime, role, at
	if runtime != RuntimeCodex {
		u.Account = accountName(account)
	}
	if w := rl.Windows.FiveHour; w != nil {
		u.FiveHour = &Window{Utilization: w.Utilization, ResetsAt: w.ResetsAt}
	}
	if w := rl.Windows.SevenDay; w != nil {
		u.SevenDay = &Window{Utilization: w.Utilization, ResetsAt: w.ResetsAt}
	}
	if rl.Windows.FiveHour == nil && rl.Type == "five_hour" && rl.ResetsAt > 0 {
		u.FiveHour = &Window{Utilization: rl.Utilization, ResetsAt: rl.ResetsAt}
	}
	if rl.Windows.SevenDay == nil && rl.Type == "seven_day" && rl.ResetsAt > 0 {
		u.SevenDay = &Window{Utilization: rl.Utilization, ResetsAt: rl.ResetsAt}
	}
	switch {
	case rl.Status == "rejected" && !rl.UsingOverage && rl.OverageStatus != "allowed" && rl.OverageStatus != "allowed_warning":
		u.Status, u.OutUntil = UsageOut, rl.ResetsAt
	case rl.Status == "allowed_warning":
		u.Status, u.OutUntil = UsageWarning, 0
	case rl.Status == "allowed" || rl.Status == "rejected":
		u.Status, u.OutUntil = UsageOK, 0
	}
	if u.Status == "" {
		u.Status = UsageOK
	}
	s.m[key] = u
	s.saveLocked()
}

// MarkOut records that key's account ran out (a turn stopped at its limit).
func (s *UsageStore) MarkOut(runtime, account, role string, until int64, at time.Time) {
	key := UsageKey(runtime, account)
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.m[key]
	u.Runtime, u.Role, u.At, u.Status, u.OutUntil = runtime, role, at, UsageOut, until
	if runtime != RuntimeCodex {
		u.Account = accountName(account)
	}
	s.m[key] = u
	s.saveLocked()
}

// Get is key's latest usage.
func (s *UsageStore) Get(key string) (Usage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.m[key]
	return u, ok
}

// All is every key's usage as of now.
func (s *UsageStore) All(now time.Time) map[string]Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Usage, len(s.m))
	for k, u := range s.m {
		out[k] = u.Current(now)
	}
	return out
}

func (s *UsageStore) saveLocked() {
	raw, err := json.MarshalIndent(s.m, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.path), 0o700)
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, s.path)
	}
}
