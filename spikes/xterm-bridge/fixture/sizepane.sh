#!/bin/sh
# Non-chat pane: prints its size on start and on every SIGWINCH, echoes input.
name=$1
show() { printf '%s SIZE %sx%s\n' "$name" "$(tput cols)" "$(tput lines)"; }
trap show WINCH
show
while :; do
  if read -r line; then printf '%s IN %s\n' "$name" "$line"; fi
done
