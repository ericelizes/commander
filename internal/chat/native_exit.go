package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type nativeExitReceipt struct {
	Limit      *UsageLimit `json:"usage_limit,omitempty"`
	Summary    string      `json:"summary,omitempty"`
	Role       string      `json:"role"`
	RunID      int64       `json:"run_id"`
	SessionID  string      `json:"session_id"`
	Status     string      `json:"status"`
	Code       int         `json:"code"`
	Signal     string      `json:"signal,omitempty"`
	ObservedAt int64       `json:"observed_at"`
}

func nativeExitPath(home, role string, rid int64) string {
	return filepath.Join(home, "chat", "native-exits", fmt.Sprintf("%s.%d.json", role, rid))
}

// The host, not the daemon, bounds an execution to one completed turn. Closing
// stdin and escalation continue even while the daemon is restarting.
func (h *host) finishNativeTurn(raw []byte) {
	h.nativeEndOnce.Do(func() {
		var result struct {
			IsError bool   `json:"is_error"`
			Summary string `json:"result"`
		}
		_ = json.Unmarshal(raw, &result)
		h.nativeResultError = result.IsError
		h.nativeResultSummary = result.Summary
		if len(h.nativeResultSummary) > 6000 {
			h.nativeResultSummary = h.nativeResultSummary[:6000]
		}
		go func() {
			if h.stdin != nil {
				h.wmu.Lock()
				_ = h.stdin.Close()
				h.wmu.Unlock()
			}
			for _, signal := range []syscall.Signal{0, syscall.SIGTERM, syscall.SIGKILL} {
				if signal != 0 {
					h.mu.Lock()
					gone := h.exited != nil
					h.mu.Unlock()
					if gone {
						return
					}
					_ = h.cmd.Process.Signal(signal)
				}
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					h.mu.Lock()
					gone := h.exited != nil
					h.mu.Unlock()
					if gone {
						return
					}
					time.Sleep(20 * time.Millisecond)
				}
			}
		}()
	})
}
func (h *host) persistNativeExit(exit *hostExit) {
	if h.cfg.Info.ExecutionRunID <= 0 || h.cfg.ExitReceiptPath == "" {
		return
	}
	status := "exited"
	if exit.Code != 0 || exit.Signal != "" || h.nativeResultError {
		status = "native-error"
	}
	if h.nativeLimit != nil && (h.nativeLimit.fromText || h.nativeResultError) {
		status = "usage-limited"
		h.nativeLimit.Runtime = h.cfg.Info.Runtime
		h.nativeLimit.Account = h.cfg.Info.Account
		h.nativeLimit.At = time.Now()
	} else {
		h.nativeLimit = nil
	}
	receipt := nativeExitReceipt{Limit: h.nativeLimit, Summary: h.nativeResultSummary, Role: h.cfg.Info.Role, RunID: h.cfg.Info.ExecutionRunID, SessionID: h.cfg.Info.SessionID, Status: status, Code: exit.Code, Signal: exit.Signal, ObservedAt: time.Now().Unix()}
	raw, e := json.Marshal(receipt)
	if e == nil {
		tmp := h.cfg.ExitReceiptPath + ".tmp"
		var file *os.File
		file, e = os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if e == nil {
			_, e = file.Write(raw)
			if e == nil {
				e = file.Sync()
			}
			closeErr := file.Close()
			if e == nil {
				e = closeErr
			}
		}
		if e == nil {
			e = os.Rename(tmp, h.cfg.ExitReceiptPath)
		}
		if e == nil {
			var dir *os.File
			dir, e = os.Open(filepath.Dir(h.cfg.ExitReceiptPath))
			if e == nil {
				e = dir.Sync()
				_ = dir.Close()
			}
		}
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, "native execution exit receipt:", e)
	}
}

// Socket loss means custody is unknown, not that the model exited. Preserve
// restart intent and let a later attachment/process observation recover it.
func (e *Engine) nativeDisconnected(c *chatRole, p *process) {
	c.mu.Lock()
	owns := c.proc == p
	if owns {
		c.proc = nil
		c.meta.Running = true
		_ = writeMeta(e.logDir(), c.role, c.meta)
	}
	p.detached = true
	c.mu.Unlock()
	if owns {
		e.releaseSession(p.spec.SessionID)
	}
	close(p.done)
	p.hc.close()
	if owns {
		c.publishBound(p, true, Event{Kind: KindNotice, Source: SourceEngine, SessionID: p.spec.SessionID, Subtype: "native_connection_lost", Text: "Execution connection lost; process exit is unverified and its claim remains recorded."})
	}
}
func (e *Engine) flushNativeExits(ctx context.Context, role string) error {
	pattern := filepath.Join(e.opts.Home, "chat", "native-exits", "*.json")
	if role != "" {
		pattern = filepath.Join(e.opts.Home, "chat", "native-exits", role+".*.json")
	}
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	sink, ok := e.opts.States.(interface {
		FinishNativeExecution(context.Context, string, int64, string, string) error
	})
	if !ok {
		return errors.New("native exit sink has no run-aware completion")
	}
	var firstErr error
	for i, path := range paths {
		if i == 128 {
			break
		}
		if ctx.Err() != nil {
			return errors.Join(firstErr, ctx.Err())
		}
		raw, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		var receipt nativeExitReceipt
		if err = json.Unmarshal(raw, &receipt); err != nil || !ValidRole(receipt.Role) || receipt.RunID <= 0 || receipt.SessionID == "" || receipt.ObservedAt <= 0 || receipt.Status != "exited" && receipt.Status != "native-error" && receipt.Status != "usage-limited" || filepath.Clean(path) != nativeExitPath(e.opts.Home, receipt.Role, receipt.RunID) {
			e.opts.Logf("chat: invalid native exit receipt %s", filepath.Base(path))
			if moveErr := os.Rename(path, fmt.Sprintf("%s.invalid-%d", path, time.Now().UnixNano())); moveErr != nil && firstErr == nil {
				firstErr = moveErr
			}
			continue
		}

		// Persist capacity evidence before the exit transaction makes recovery claimable.
		if receipt.Status == "usage-limited" && receipt.Limit != nil {
			u := receipt.Limit
			e.opts.Usage.MarkOut(u.Runtime, u.Account, receipt.Role, u.ResetsAt, u.At)
		}
		if reports, ok := e.opts.States.(interface {
			FinishNativeExecutionReport(context.Context, string, int64, string, string, string) error
		}); ok {
			err = reports.FinishNativeExecutionReport(ctx, receipt.Role, receipt.RunID, receipt.SessionID, receipt.Status, receipt.Summary)
		} else {
			err = sink.FinishNativeExecution(ctx, receipt.Role, receipt.RunID, receipt.SessionID, receipt.Status)
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		c, err := e.chat(receipt.Role)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		c.mu.Lock()
		if c.meta.ExecutionRunID == receipt.RunID && c.meta.SessionID == receipt.SessionID {
			c.meta.Running = false
			_ = writeMeta(e.logDir(), c.role, c.meta)
		}
		c.mu.Unlock()
		if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// CodexThreadFileForRun follows the native host's recorded immutable binding.
// Raw host operators retain their existing role thread file; another run's
// native metadata cannot redirect their checkpoint to a different conversation.
func CodexThreadFileForRun(home, role string, rid int64, session string) string {
	metadata := readMeta(filepath.Join(home, "chat"), role)
	if rid > 0 && metadata.ExecutionRunID == rid && metadata.SessionID == session {
		return (Spec{Role: role, ExecutionRunID: rid, SessionID: session}).codexThreadFile(home)
	}
	return CodexThreadFile(home, role)
}
