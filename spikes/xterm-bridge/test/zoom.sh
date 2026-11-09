#!/bin/sh
# Which tmux operations silently unzoom a window? (cmdr's __relayout hook runs
# resize-pane on every pane on client-attached/client-resized.)
SOCK=${SOCK:-/tmp/cl.s1/sock}
t() { env -u TMUX tmux -S "$SOCK" "$@"; }
t new-session -d -s z -x 200 -y 50
t split-window -h -t z:0
t split-window -h -t z:0
z() { t display -p -t z:0 "$1: zoomed=#{window_zoomed_flag} chat=#{pane_width}x#{pane_height}"; }
rez() { [ "$(t display -p -t z:0 '#{window_zoomed_flag}')" = 1 ] || t resize-pane -Z -t z:0.1; }
rez; z 'zoomed chat (pane 1)'
t resize-pane -t z:0.0 -x 30; z 'resize-pane -x 30 on a hidden pane'
rez; t resize-pane -t z:0.1 -x 30; z 'resize-pane -x 30 on the zoomed pane'
rez; t select-layout -t z:0 even-horizontal; z 'select-layout'
rez; t split-window -d -h -t z:0.0; z 'split-window -d (new pane)'
rez; t select-pane -t z:0.0; z 'select-pane to another pane'
rez; t respawn-pane -k -t z:0.0 'sleep 100'; z 'respawn-pane -k of a hidden pane'
rez; t kill-pane -t z:0.3; z 'kill-pane of a hidden pane'
rez; t set -w -t z:0 window-style bg=red; z 'set-option window-style'
t kill-session -t z
