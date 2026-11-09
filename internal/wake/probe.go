package wake

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/example-user/commander/internal/layout"
)

// ErrDeferred means the pane is not safe to type into right now. It is not a
// failure: the Waker retries on its next poll without logging.
var ErrDeferred = fmt.Errorf("pane busy")

// InputBusyError says the input box has text in it. It wraps ErrDeferred, and
// carries what the text is and how long it has been unchanged, so the Waker can
// tell a stray character from a half-written message.
type InputBusyError struct {
	Text      string
	StableFor time.Duration
}

func (e *InputBusyError) Error() string { return ErrDeferred.Error() + ": input box is not empty" }
func (e *InputBusyError) Unwrap() error { return ErrDeferred }

// QuietPeriod is how long the input line must have been untouched before the
// probe trusts that nobody is typing.
const QuietPeriod = 10 * time.Second

// TmuxFunc runs `tmux args...` and returns stdout.
type TmuxFunc func(ctx context.Context, args ...string) ([]byte, error)

func defaultTmux(ctx context.Context, args ...string) ([]byte, error) {
	out, stderr, err := layout.DefaultRunner{}.Run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("tmux %s: %w: %s", args[0], err, strings.TrimSpace(string(stderr)))
	}
	return out, nil
}

// Probe decides whether it is safe to type into a Claude Code chat pane.
//
// Typing into a pane is only safe when ALL of these hold:
//
//  1. No turn is in progress. While generating, Claude Code shows
//     "esc to interrupt" in its footer; typed text would be queued as if the
//     user said it, and Enter could interrupt tool use.
//  2. The input box is empty. The box sits between two "─" rules and starts
//     with a "❯" prompt. When empty it shows a dim (SGR 2, "faint") placeholder
//     such as `Try "how does <filepath> work?"`; dim text does not count as
//     input, which is why the capture uses -e to keep escapes.
//  3. Nobody has been typing recently. We remember the input text seen at the
//     previous probe of this pane; any change (typed, deleted, submitted)
//     restarts a QuietPeriod during which we defer. On top of that, if the
//     pane's window is the one an attached client is looking at, a client
//     keystroke within QuietPeriod (#{client_activity}) defers too.
//
// Anything we cannot recognise (a permission dialog, a different screen, no
// input box found) is treated as unsafe: when in doubt, do not type.
type Probe struct {
	Tmux  TmuxFunc
	Now   func() time.Time
	Quiet time.Duration

	mu   sync.Mutex
	seen map[string]inputSeen
}

type inputSeen struct {
	text      string
	changedAt time.Time
}

// NewProbe returns a Probe that talks to the real tmux.
func NewProbe() *Probe { return &Probe{Tmux: defaultTmux} }

// Check returns nil when pane is safe to type into, an error wrapping
// ErrDeferred (with the reason) when not, or another error if tmux failed.
func (p *Probe) Check(ctx context.Context, pane string) error {
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	quiet := p.Quiet
	if quiet == 0 {
		quiet = QuietPeriod
	}

	out, err := p.Tmux(ctx, "capture-pane", "-p", "-e", "-t", pane)
	if err != nil {
		return err
	}
	state := ParseScreen(string(out))
	if state.Busy {
		return deferred("turn in progress")
	}
	if !state.HasInputBox {
		return deferred("no input box on screen")
	}
	if state.Input != "" {
		stable := p.remember(pane, state.Input, now())
		return &InputBusyError{Text: state.Input, StableFor: stable}
	}
	if p.recentlyChanged(pane, state.Input, now(), quiet) {
		return deferred("input changed recently")
	}
	act, err := p.Tmux(ctx, "display-message", "-p", "-t", pane, "#{window_active}|#{session_attached}|#{client_activity}")
	if err != nil {
		return err
	}
	if clientTypedRecently(strings.TrimSpace(string(act)), now(), quiet) {
		return deferred("client active in this window")
	}
	return nil
}

func deferred(reason string) error { return fmt.Errorf("%w: %s", ErrDeferred, reason) }

// remember records the current input text, bumping changedAt when it differs.
// It returns how long the text has been the same.
func (p *Probe) remember(pane, text string, now time.Time) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.seen == nil {
		p.seen = map[string]inputSeen{}
	}
	if prev, ok := p.seen[pane]; !ok || prev.text != text {
		p.seen[pane] = inputSeen{text: text, changedAt: now}
		return 0
	}
	return now.Sub(p.seen[pane].changedAt)
}

// recentlyChanged records text and reports whether it changed within quiet.
// The first ever observation of an empty input is not a change.
func (p *Probe) recentlyChanged(pane, text string, now time.Time, quiet time.Duration) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.seen == nil {
		p.seen = map[string]inputSeen{}
	}
	prev, ok := p.seen[pane]
	if !ok {
		p.seen[pane] = inputSeen{text: text}
		return false
	}
	if prev.text != text {
		p.seen[pane] = inputSeen{text: text, changedAt: now}
		return true
	}
	return !prev.changedAt.IsZero() && now.Sub(prev.changedAt) < quiet
}

// clientTypedRecently parses "<window_active>|<session_attached>|<client_activity epoch>".
// Without an attached client (detached cockpit) nobody can be typing.
func clientTypedRecently(format string, now time.Time, quiet time.Duration) bool {
	parts := strings.Split(format, "|")
	if len(parts) != 3 || parts[0] != "1" || parts[1] == "" || parts[1] == "0" || parts[2] == "" {
		return false
	}
	var epoch int64
	if _, err := fmt.Sscanf(parts[2], "%d", &epoch); err != nil {
		return false
	}
	return now.Sub(time.Unix(epoch, 0)) < quiet
}

// Screen is what the probe understood from one capture.
type Screen struct {
	Busy        bool   // "esc to interrupt" in the footer
	HasInputBox bool   // a "❯" line between two "─" rules was found
	Input       string // non-dim text typed into the box ("" for the placeholder)
}

// ParseScreen reads a `capture-pane -p -e` dump of a Claude Code chat.
func ParseScreen(capture string) Screen {
	lines := strings.Split(strings.TrimRight(capture, "\n"), "\n")
	plain := make([]string, len(lines))
	solid := make([]string, len(lines)) // text with dim (faint) runs removed
	for i, l := range lines {
		plain[i], solid[i] = stripSGR(l)
	}

	var s Screen
	// The footer is the last few non-empty lines; scanning only those keeps
	// a reply that merely quotes the phrase from looking like a live turn.
	seen := 0
	for i := len(plain) - 1; i >= 0 && seen < 4; i-- {
		if strings.TrimSpace(plain[i]) == "" {
			continue
		}
		seen++
		if strings.Contains(plain[i], "esc to interrupt") {
			s.Busy = true
		}
	}

	// Input box: the last "❯" line that has a rule line right above it.
	for i := len(plain) - 1; i > 0; i-- {
		if !strings.HasPrefix(strings.TrimSpace(plain[i]), "❯") || !isRule(plain[i-1]) {
			continue
		}
		end := -1
		for j := i + 1; j < len(plain); j++ {
			if isRule(plain[j]) {
				end = j
				break
			}
		}
		if end < 0 {
			continue
		}
		s.HasInputBox = true
		var typed []string
		for k := i; k < end; k++ {
			text := strings.TrimSpace(solid[k])
			if k == i {
				text = strings.TrimSpace(strings.TrimPrefix(text, "❯"))
			}
			if text != "" {
				typed = append(typed, text)
			}
		}
		s.Input = strings.Join(typed, "\n")
		break
	}
	return s
}

func isRule(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "──") }

// stripSGR removes terminal escapes from line. plain keeps all text; solid
// drops text drawn while the faint attribute (SGR 2) is on, which is how
// Claude Code renders its input placeholder.
func stripSGR(line string) (plain, solid string) {
	var p, s strings.Builder
	faint := false
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		if rs[i] != 0x1b {
			p.WriteRune(rs[i])
			if !faint {
				s.WriteRune(rs[i])
			}
			continue
		}
		if i+1 >= len(rs) || rs[i+1] != '[' {
			continue
		}
		j := i + 2
		for j < len(rs) && (rs[j] < 0x40 || rs[j] > 0x7e) {
			j++
		}
		if j < len(rs) && rs[j] == 'm' {
			faint = applySGR(faint, string(rs[i+2:j]))
		}
		i = j
	}
	return p.String(), s.String()
}

// applySGR updates the faint flag for one "\x1b[<params>m" sequence.
func applySGR(faint bool, params string) bool {
	if params == "" {
		return false // ESC[m is a full reset
	}
	codes := strings.Split(params, ";")
	for i := 0; i < len(codes); i++ {
		switch codes[i] {
		case "38", "48", "58":
			// Extended color: 38;5;<n> or 38;2;<r>;<g>;<b>. Skip its arguments
			// so a color index of 2 is not mistaken for the faint attribute.
			if i+1 < len(codes) && codes[i+1] == "5" {
				i += 2
			} else if i+1 < len(codes) && codes[i+1] == "2" {
				i += 4
			}
		case "0", "":
			faint = false
		case "2":
			faint = true
		case "22":
			faint = false
		}
	}
	return faint
}
