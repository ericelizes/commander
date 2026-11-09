#!/bin/sh
# Build a fake cockpit on a PRIVATE tmux server (never the live `commander`).
#   session `commander`, window 1 `manager (demo)` = sidebar | chat | context
#   the chat pane carries @cmdr_chat=1, like the real cockpit.
# Usage: SOCK=/tmp/cl.s1/sock fixture/up.sh [--mouse]
set -eu
here=$(cd "$(dirname "$0")" && pwd)
SOCK=${SOCK:-/tmp/cl.s1/sock}
mkdir -p "$(dirname "$SOCK")"
t() { env -u TMUX tmux -S "$SOCK" "$@"; }
t kill-server 2>/dev/null || true
t -f /dev/null new-session -d -s commander -x 200 -y 50 -n root "sh $here/sizepane.sh root"
t set -g default-terminal tmux-256color
# Mirror the user's ~/.tmux.conf bits that matter to a bridged client, and the
# root binding cmdr itself installs (C-g -> fleet window).
t set -g prefix C-a
t set -g mouse on
t set -s escape-time 0
t set -ga terminal-overrides ',*256col*:Tc'
t bind -n C-g display-message 'ROOT C-g intercepted'
t bind -n S-Left previous-window
t new-window -t commander: -n 'manager (demo)' "sh $here/sizepane.sh sidebar"
t split-window -h -t 'commander:manager (demo)' "python3 $here/fakechat.py ${1:-}"
t set -p @cmdr_chat 1
t split-window -h -t 'commander:manager (demo)' "sh $here/sizepane.sh context"
t select-layout -t 'commander:manager (demo)' even-horizontal
t list-panes -a -F '#{session_name}:#{window_index} #{pane_id} #{pane_width}x#{pane_height} chat=#{@cmdr_chat} #{pane_current_command}'
