package layout

import (
	"context"
	"fmt"
	"strings"
)

// PadOption is the pane option marking a spacer pane: "h" for a one-column
// stripe beside the chat, "v" for a one-row stripe above or below it.
const PadOption = "@cmdr_pad"

// addPads surrounds the chat pane with three one-cell spacer panes (left, right,
// below; the border under the title bar already gives the top one row of air). tmux has no
// pane padding and we do not render Claude's screen, so the padding is real
// panes on the chat background; pane borders are invisible (fg == bg), so the
// chat gets two cells of air on each side without any line between the spacer
// and the text. Spacers never take focus or input.
func (d *Director) addPads(ctx context.Context, chatPane, cwd string) error {
	pad := d.buildColumnCmd("pad")
	for _, sp := range []struct {
		kind  string
		flags []string
	}{
		{"v", []string{"-v"}},       // below the chat
		{"h", []string{"-h", "-b"}}, // left
		{"h", []string{"-h"}},       // right
	} {
		args := append([]string{"split-window", "-d", "-l", "1", "-P", "-F", "#{pane_id}", "-t", chatPane}, sp.flags...)
		if cwd != "" {
			args = append(args, "-c", cwd)
		}
		stdout, stderr, err := d.r.Run(ctx, append(args, pad)...)
		if err != nil {
			return fmt.Errorf("tmux split-window pad: %w: %s", err, strings.TrimSpace(string(stderr)))
		}
		id := strings.TrimSpace(string(stdout))
		if id == "" {
			continue
		}
		for _, cmd := range [][]string{
			{"set-option", "-p", "-t", id, PadOption, sp.kind},
			{"select-pane", "-d", "-t", id}, // never focusable
		} {
			if _, stderr, err := d.r.Run(ctx, cmd...); err != nil {
				return fmt.Errorf("tmux %s: %w: %s", cmd[0], err, strings.TrimSpace(string(stderr)))
			}
		}
	}
	return nil
}
