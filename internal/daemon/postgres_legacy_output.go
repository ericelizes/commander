package daemon

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
)

// Surviving pre-cutover Operators retain their launch-time hooks. Read their
// existing normalized stdout until they finish; no process restart is needed.
// New launches use immutable run hooks and never take this compatibility path.
func (d *Daemon) observeLegacyPostgresOperators(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for ctx.Err() == nil {
		work, cancel := context.WithTimeout(ctx, 3*time.Second)
		runs, e := d.store.ListRunningRuns(work)
		if e == nil {
			for _, run := range runs {
				p, e := d.store.GetProject(work, run.ProjectID)
				if e != nil || p == nil {
					continue
				}
				launcher := filepath.Join(p.Path, ".commander", "operators", run.AgentName+".sh")
				raw, e := os.ReadFile(launcher)
				if e != nil || strings.Contains(string(raw), "COMMANDER_RUN_ID=") {
					continue
				}
				d.readLegacyOperatorOutput(work, run, p.Path)
			}
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (d *Daemon) readLegacyOperatorOutput(ctx context.Context, run store.AgentRun, projectPath string) {
	path := spawn.RunOutputPath(run, projectPath)
	f, e := os.Open(path)
	if e != nil {
		return
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() {
		return
	}
	var offset int64
	_ = d.store.DB().QueryRowContext(ctx, `SELECT byte_offset FROM legacy_operator_cursors WHERE run_id=?`, run.ID).Scan(&offset)
	if offset > info.Size() {
		return
	}
	if offset == info.Size() {
		return
	}
	if info.Size()-offset > 1<<20 {
		offset = info.Size() - (1 << 20)
		if _, e = f.Seek(offset, io.SeekStart); e != nil {
			return
		}
		reader := bufio.NewReader(f)
		part, e := reader.ReadBytes('\n')
		if e != nil {
			return
		}
		offset += int64(len(part))
	}
	if _, e = f.Seek(offset, io.SeekStart); e != nil {
		return
	}
	reader := bufio.NewReader(io.LimitReader(f, 1<<20))
	normal := chat.NewNormalizer(0, false, time.Now)
	expected := run.OpencodeSessionID
	if thread, e := os.ReadFile(chat.CodexThreadFile(d.home, run.AgentName)); e == nil && strings.TrimSpace(string(thread)) != "" {
		expected = strings.TrimSpace(string(thread))
	}
	state, detail, evidence := "", "", ""
	enqueue := false
	for ctx.Err() == nil {
		line, e := reader.ReadBytes('\n')
		if e != nil {
			break
		}
		offset += int64(len(line))
		events, _ := normal.Line(line)
		for _, ev := range events {
			if ev.ParentToolUseID != "" {
				continue
			}
			if ev.SessionID != "" && ev.SessionID != expected && ev.SessionID != run.OpencodeSessionID {
				continue
			}
			next := ""
			switch ev.Kind {
			case chat.KindToolStart, chat.KindTextDelta, chat.KindThinkingDelta, chat.KindText, chat.KindThinking:
				next = store.AgentWorking
				if ev.Tool != nil {
					detail = ev.Tool.Name
				}
			case chat.KindResult:
				next = store.AgentIdle
			default:
				continue
			}
			state = next
			if ev.Kind == chat.KindText {
				evidence += ev.Text + "\n"
				if len(evidence) > 6000 {
					evidence = evidence[len(evidence)-6000:]
				}
			}
			if ev.Kind == chat.KindResult {
				enqueue = true
				if ev.Text != "" {
					evidence += ev.Text
				}
				if len(evidence) > 6000 {
					evidence = evidence[len(evidence)-6000:]
				}
			}
		}
	}
	if state != "" {
		if e = d.store.SetAgentStateForRun(ctx, run.AgentName, run.ID, run.OpencodeSessionID, state, detail); e != nil {
			return
		}
	}
	if evidence != "" || enqueue {
		if e = d.store.ObserveClassification(ctx, run.AgentName, run.OpencodeSessionID, offset, evidence, enqueue); e != nil {
			return
		}
	}
	_, _ = d.store.DB().ExecContext(ctx, `INSERT INTO legacy_operator_cursors(run_id,byte_offset) VALUES(?,?) ON CONFLICT(run_id) DO UPDATE SET byte_offset=excluded.byte_offset`, run.ID, offset)
}
