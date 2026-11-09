#!/bin/sh
# Alternative to zoom: move the chat pane into its own window for the app
# (break-pane) and back into the TUI layout (join-pane). Same process, its own
# window size, no shared zoom flag. Run after fixture/up.sh.
SOCK=${SOCK:-/tmp/cl.s1/sock}
t() { env -u TMUX tmux -S "$SOCK" "$@"; }
chat=$(t list-panes -t commander:1 -F '#{pane_id} #{@cmdr_chat}' | awk '$2==1{print $1}')
pid=$(t display -p -t "$chat" '#{pane_pid}')
echo "before: chat=$chat pid=$pid in $(t display -p -t "$chat" '#{window_id} #{pane_width}x#{pane_height}')"
t break-pane -d -s "$chat" -n app-chat
echo "break-pane: chat now in $(t display -p -t "$chat" '#{window_id} #{window_name} #{pane_width}x#{pane_height}') pid=$(t display -p -t "$chat" '#{pane_pid}')"
echo "            TUI window panes: $(t list-panes -t commander:1 -F '#{pane_id}=#{pane_width}x#{pane_height}' | tr '\n' ' ')"
t resize-window -t "$chat" -x 90 -y 28
echo "resize app window to 90x28: chat $(t display -p -t "$chat" '#{pane_width}x#{pane_height}'), TUI panes: $(t list-panes -t commander:1 -F '#{pane_id}=#{pane_width}x#{pane_height}' | tr '\n' ' ')"
t set-option -w -u -t "$chat" window-size
t join-pane -d -h -s "$chat" -t commander:1.0
echo "join-pane back: chat in $(t display -p -t "$chat" '#{window_id} #{pane_width}x#{pane_height}') pid=$(t display -p -t "$chat" '#{pane_pid}') @cmdr_chat=$(t display -p -t "$chat" '#{@cmdr_chat}')"
