package layout

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/example-user/commander/internal/agentrt"
	promptassets "github.com/example-user/commander/prompts"
)

// nativeChatMarker starts the placeholder command of a chat pane handed over to
// cmdrd's native chat engine (chat.PlaceholderMarker).
const nativeChatMarker = "CMDR_NATIVE_CHAT="

// sessionFlagRE matches the Claude session flag and captures the id.
var sessionFlagRE = regexp.MustCompile(`\s--(?:resume|session-id)\s+(\S+)`)

// launchArgvChanged reports whether a chat pane would be started differently
// today than it was. --resume vs --session-id is not a difference (a freshly
// minted id flips to --resume after its first message), but the session id and
// every other argument are.
func launchArgvChanged(current, desired string) bool {
	norm := func(s string) (rest, id string) {
		if m := sessionFlagRE.FindStringSubmatch(s); m != nil {
			id = m[1]
		}
		return strings.TrimSpace(sessionFlagRE.ReplaceAllString(s, "")), id
	}
	cr, ci := norm(current)
	dr, di := norm(desired)
	return cr != dr || ci != di
}

// plainStartCommand undoes tmux's quoting of #{pane_start_command}, so a pane's
// command compares with the string it was launched with. tmux 3.2+ passes each
// argument through args_escape: a command with a space, #, ', ;, $, { or } comes
// back in double quotes with vis(3) escapes; 3.1 and older return it raw. A
// string that is not one well-formed quoted argument is returned as is, which
// covers the raw form and several joined arguments (commander launches panes
// with one string). A raw command that is itself wholly quoted is not told apart;
// commander launches none.
func plainStartCommand(s string) string {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	body := s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c == '"' {
			return s
		}
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i == len(body) {
			return s
		}
		switch c = body[i]; c {
		case '\\':
			// Ubuntu's 3.4 prints $NAME as \\$NAME (the source says \$NAME); a real
			// backslash before $NAME is still its own \\ ahead of that.
			if i+2 < len(body) && body[i+1] == '$' && startsShellName(body[i+2]) {
				b.WriteByte('$')
				i++
				continue
			}
			b.WriteByte(c)
		case '"', '$', '~':
			b.WriteByte(c)
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'b':
			b.WriteByte('\b')
		case 'a':
			b.WriteByte('\a')
		case 'v':
			b.WriteByte('\v')
		case 'f':
			b.WriteByte('\f')
		default: // \ooo, three octal digits for any other unprintable byte
			if i+2 >= len(body) || c < '0' || c > '3' || body[i+1] < '0' || body[i+1] > '7' || body[i+2] < '0' || body[i+2] > '7' {
				return s
			}
			b.WriteByte((c-'0')<<6 | (body[i+1]-'0')<<3 | (body[i+2] - '0'))
			i += 2
		}
	}
	return b.String()
}

// startsShellName is the test tmux applies to the byte after a $ before escaping it.
func startsShellName(c byte) bool {
	return c == '_' || c == '{' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

type sessionPane struct {
	windowID, windowName, ws string
	pad                      string
	top                      int
	paneID                   string
	chatTag                  bool
	cmd                      string
}

func (p sessionPane) isColumn() bool { return strings.Contains(p.cmd, "cmdr __column") }

func (d *Director) listSessionPanes(ctx context.Context) ([]sessionPane, error) {
	stdout, stderr, err := d.r.Run(ctx, "list-panes", "-s", "-t", CommanderSessionName, "-F",
		"#{window_id}|#{window_name}|#{"+WorkstreamOption+"}|#{pane_id}|#{"+ChatPaneOption+"}|#{"+PadOption+"}|#{pane_top}|#{pane_start_command}")
	if err != nil {
		return nil, fmt.Errorf("tmux list-panes: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	var out []sessionPane
	for _, line := range nonEmptyLines(string(stdout)) {
		parts := strings.SplitN(line, "|", 8)
		if len(parts) < 8 {
			continue
		}
		var top int
		fmt.Sscanf(parts[6], "%d", &top)
		out = append(out, sessionPane{
			windowID: parts[0], windowName: parts[1], ws: parts[2],
			paneID: parts[3], chatTag: parts[4] == "1", pad: parts[5], top: top, cmd: plainStartCommand(parts[7]),
		})
	}
	return out, nil
}

// SetKeepChats makes SoftRestart leave every chat pane running even when its
// launch command changed (they pick the change up on their next restart). Use
// it to reload chrome and styling without interrupting anyone mid-turn.
func (d *Director) SetKeepChats(keep bool) { d.keepChats = keep }

// SoftRestart reloads the cockpit's chrome without touching live Claude
// sessions: every `cmdr __column` pane is respawned (so it runs the installed
// binary), missing sidebar/context columns and missing windows are rebuilt, and
// windows are renamed and renumbered. A chat pane is restarted only when its
// launch argv would now be different (e.g. a new flag), and then it resumes its
// session. With no session running it is a normal launch.
func (d *Director) SoftRestart(ctx context.Context, commanderHome string, attach bool) error {
	exists, err := d.SessionExists(ctx, CommanderSessionName)
	if err != nil {
		return err
	}
	if !exists {
		return d.LaunchCommander(ctx, commanderHome, attach)
	}
	d.reportProgress("Reloading the cockpit (running Claude sessions are kept)...")
	if err := d.ensureCommanderSessionChrome(ctx); err != nil {
		return err
	}
	if err := d.ensureCommanderKeyBindings(ctx); err != nil {
		return err
	}

	panes, err := d.listSessionPanes(ctx)
	if err != nil {
		return err
	}

	rootCmd, rootChat, err := d.commanderChatCmd(ctx, commanderHome)
	if err != nil {
		return err
	}
	wsChat := map[string]claudeChat{}
	workstreamTitles := map[string]string{}
	wsProject := map[string]string{}
	if workstreamWindowsFn != nil {
		windows, err := workstreamWindowsFn(ctx)
		if err != nil && len(windows) == 0 {
			d.reportProgress("Could not list workstreams (Manager chats left as they are): %v", err)
		} else {
			if err != nil { // skipped workstreams keep their chats as they are
				d.reportProgress("Some workstream Managers were skipped (their chats left as they are): %v", err)
			}
			for _, w := range windows {
				wsChat[w.BeadID] = claudeChat{w.AgentName, agentrt.Model(agentrt.TierLead), w.SessionID}
				workstreamTitles[w.BeadID] = w.Title
				wsProject[w.BeadID] = w.ProjectName
			}
		}
	}

	// The chat pane of each window: the tagged one, else the first non-column pane.
	chatOf := map[string]string{}
	for _, p := range panes {
		if p.chatTag {
			chatOf[p.windowID] = p.paneID
		}
	}
	for _, p := range panes {
		if _, ok := chatOf[p.windowID]; !ok && !p.isColumn() {
			chatOf[p.windowID] = p.paneID
		}
	}

	columns, restartedChats := 0, 0
	rootWindowID := ""
	for _, p := range panes {
		if p.ws == "" && (p.windowName == FleetWindowName || p.windowName == legacyFleetWindowName) {
			rootWindowID = p.windowID
		}
	}
	for _, p := range panes {
		switch {
		case p.isColumn():
			// No command: respawn-pane re-runs the pane's original command, now
			// resolved to the installed binary. The old fleet/lead-fleet column on
			// the right of Commander and workstream windows becomes the Inspector.
			args := []string{"respawn-pane", "-k", "-t", p.paneID}
			if inspector := d.inspectorCmdFor(p, rootWindowID, wsProject); inspector != "" {
				args = append(args, inspector)
			}
			if _, stderr, err := d.r.Run(ctx, args...); err != nil {
				d.reportProgress("Could not reload column %s: %v: %s", p.paneID, err, strings.TrimSpace(string(stderr)))
				continue
			}
			columns++
		case chatOf[p.windowID] == p.paneID:
			want, chat := "", (*claudeChat)(nil)
			switch {
			case p.ws != "":
				if c, ok := wsChat[p.ws]; ok {
					want, chat = d.claudeChatCmd(c), &c
				}
			case p.windowID == rootWindowID:
				want, chat = rootCmd, rootChat
			}
			if strings.HasPrefix(strings.TrimSpace(p.cmd), nativeChatMarker) {
				continue // the chat runs in cmdrd's native engine; a TUI here would fork its session
			}
			if want == "" || !launchArgvChanged(p.cmd, want) {
				continue // legacy window, unknown workstream, or nothing changed
			}
			if d.keepChats {
				d.reportProgress("Leaving the chat in %q running (its launch command changed; it updates on its next restart).", p.windowName)
				continue
			}
			if chat != nil {
				// Only now: a chat left running keeps writing where it is.
				if want, err = d.claudeLaunchCmd(ctx, *chat); err != nil {
					d.reportProgress("Leaving the chat in %q running (its session could not be carried over): %v", p.windowName, err)
					continue
				}
			}
			d.reportProgress("Restarting the chat in %q (its launch command changed; the session resumes)...", p.windowName)
			if _, stderr, err := d.r.Run(ctx, "respawn-pane", "-k", "-t", p.paneID, want); err != nil {
				return fmt.Errorf("tmux respawn-pane %s: %w: %s", p.paneID, err, strings.TrimSpace(string(stderr)))
			}
			restartedChats++
		}
	}
	d.reportProgress("Reloaded %d column panes; restarted %d chat panes.", columns, restartedChats)

	if rootWindowID != "" {
		if err := d.repairRootColumns(ctx, panes, rootWindowID, chatOf[rootWindowID]); err != nil {
			return err
		}
	}
	d.ensureTitleBars(ctx, panes, chatOf, rootWindowID, workstreamTitles)
	d.ensurePads(ctx, panes, chatOf, rootWindowID)
	for windowID, chat := range chatOf {
		if err := d.applyWindowStyle(ctx, chat); err != nil {
			d.reportProgress("Could not restyle %s: %v", windowID, err)
		}
	}
	if err := d.ensureLinkedManagerWindows(ctx); err != nil {
		return err
	}
	if err := d.Relayout(ctx); err != nil {
		d.reportProgress("Could not re-apply column widths: %v", err)
	}
	if attach {
		// Inside tmux we are reloading under the viewer's feet: leave whatever
		// window they are looking at selected. Only a launch from outside tmux
		// selects Commander and attaches.
		if d.insideTmuxFn != nil && d.insideTmuxFn() {
			return nil
		}
		if _, stderr, runErr := d.r.Run(ctx, "select-window", "-t", CommanderSessionName+":"+FleetWindowName); runErr != nil {
			return fmt.Errorf("tmux select-window: %w: %s", runErr, strings.TrimSpace(string(stderr)))
		}
		return d.attachOrSwitch(ctx, CommanderSessionName)
	}
	return nil
}

// repairRootColumns re-adds the sidebar and context columns of the Commander
// window if a pane has gone missing (e.g. it was closed), without touching chat.
func (d *Director) repairRootColumns(ctx context.Context, panes []sessionPane, rootWindowID, chatPane string) error {
	if chatPane == "" {
		return nil
	}
	hasSidebar, hasFleet := false, false
	for _, p := range panes {
		if p.windowID != rootWindowID {
			continue
		}
		hasSidebar = hasSidebar || strings.Contains(p.cmd, "cmdr __column cmdr-objectives")
		hasFleet = hasFleet || isRightContextColumn(p.cmd)
	}
	if hasSidebar && hasFleet {
		return nil
	}
	workspaceRoot, err := promptassets.CommanderWorkspaceRoot()
	if err != nil {
		return fmt.Errorf("commander workspace root: %w", err)
	}
	if !hasSidebar {
		d.reportProgress("Restoring the Commander window's sidebar...")
		if _, stderr, err := d.r.Run(ctx, "split-window", "-h", "-b", "-d", "-P", "-F", "#{pane_id}", "-t", chatPane, "-p", "25", "-c", workspaceRoot, d.buildColumnCmd("cmdr-objectives")); err != nil {
			return fmt.Errorf("tmux split-window sidebar: %w: %s", err, strings.TrimSpace(string(stderr)))
		}
	}
	if !hasFleet {
		d.reportProgress("Restoring the Commander window's context column...")
		if _, stderr, err := d.r.Run(ctx, "split-window", "-h", "-d", "-P", "-F", "#{pane_id}", "-t", chatPane, "-p", "33", "-c", workspaceRoot, d.buildColumnCmd("inspector")); err != nil {
			return fmt.Errorf("tmux split-window fleet: %w: %s", err, strings.TrimSpace(string(stderr)))
		}
	}
	return nil
}

// ensureTitleBars gives every Commander / workstream window its title bar (and
// the options it reads) if it has none yet. Windows built before the bar existed
// get it here, above their chat, without touching the chat itself.
func (d *Director) ensureTitleBars(ctx context.Context, panes []sessionPane, chatOf map[string]string, rootWindowID string, titles map[string]string) {
	hasBar := map[string]bool{}
	ws := map[string]string{} // window id -> workstream bead
	for _, p := range panes {
		if strings.Contains(p.cmd, "cmdr __column chat-title") {
			hasBar[p.windowID] = true
		}
		if p.ws != "" {
			ws[p.windowID] = p.ws
		}
	}
	for windowID, chat := range chatOf {
		kind, title := "", ""
		switch {
		case windowID == rootWindowID:
			kind, title = "commander", "Commander"
		case ws[windowID] != "":
			kind = "manager"
			if t := titles[ws[windowID]]; t != "" {
				title = ManagerTitle(t)
			}
		default:
			continue // legacy per-project / operator windows keep their layout
		}
		if title != "" {
			if err := d.setWindowTitle(ctx, chat, kind, title); err != nil {
				d.reportProgress("Could not set the title of %s: %v", windowID, err)
			}
			// Windows built before the meta option existed get it from the chat's cwd.
			if out, _, err := d.r.Run(ctx, "display-message", "-p", "-t", chat, "#{pane_current_path}"); err == nil {
				if cwd := strings.TrimSpace(string(out)); strings.HasPrefix(cwd, "/") {
					if err := d.setWindowMeta(ctx, chat, kind, cwd); err != nil {
						d.reportProgress("Could not set the meta of %s: %v", windowID, err)
					}
				}
			}
		}
		if hasBar[windowID] {
			continue
		}
		if err := d.addTitleBar(ctx, chat, ""); err != nil {
			d.reportProgress("Could not add the title bar to %s: %v", windowID, err)
		}
	}
}

// ensurePads gives Commander and workstream windows their spacer panes (left,
// right, below the chat) without touching the chat itself, and removes a stray
// spacer sitting above the chat (an earlier layout had one; the title bar's
// border already gives the top its air).
func (d *Director) ensurePads(ctx context.Context, panes []sessionPane, chatOf map[string]string, rootWindowID string) {
	hasPad := map[string]bool{}
	managed := map[string]bool{}
	chatTop := map[string]int{}
	for _, p := range panes {
		if p.pad != "" {
			hasPad[p.windowID] = true
		}
		if p.ws != "" {
			managed[p.windowID] = true
		}
		if chatOf[p.windowID] == p.paneID {
			chatTop[p.windowID] = p.top
		}
	}
	for _, p := range panes {
		if p.pad == "v" && p.top < chatTop[p.windowID] {
			if _, _, err := d.r.Run(ctx, "kill-pane", "-t", p.paneID); err != nil {
				d.reportProgress("Could not remove the top spacer %s: %v", p.paneID, err)
			}
		}
	}
	for windowID, chat := range chatOf {
		if hasPad[windowID] || (windowID != rootWindowID && !managed[windowID]) {
			continue
		}
		if err := d.addPads(ctx, chat, ""); err != nil {
			d.reportProgress("Could not add padding to %s: %v", windowID, err)
		}
	}
}

// inspectorCmdFor returns the inspector launch command when pane p is a legacy
// fleet column that the Inspector replaces (Commander window: cmdr-fleet;
// workstream window: lead-fleet), or "" to respawn p unchanged.
func (d *Director) inspectorCmdFor(p sessionPane, rootWindowID string, wsProject map[string]string) string {
	switch {
	case p.windowID == rootWindowID && strings.Contains(p.cmd, "cmdr __column cmdr-fleet"):
		return d.buildColumnCmd("inspector")
	case p.ws != "" && strings.Contains(p.cmd, "cmdr __column lead-fleet") && wsProject[p.ws] != "":
		return d.buildColumnCmdWithFlags("inspector", "--ws", p.ws, "--project", wsProject[p.ws])
	}
	return ""
}
