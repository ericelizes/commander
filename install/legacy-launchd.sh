# Compatibility for installations made before service labels became neutral.
# Sourced only by install.sh; never stop a running keeper (it owns Operators).
legacy_keeper_loaded() {
    launchctl print "gui/$(id -u)/com.example.commander-tmux" >/dev/null 2>&1
}
cleanup_legacy_daemon() {
    launchctl bootout "gui/$(id -u)/com.example.commander" 2>/dev/null || true
    rm -f "$HOME/Library/LaunchAgents/com.example.commander.plist"
}
retire_legacy_keeper_plist() {
    # Removing its login entry leaves the loaded job alive until logout. The
    # neutral plist written by install.sh takes over at the next login.
    rm -f "$HOME/Library/LaunchAgents/com.example.commander-tmux.plist"
}

kick_legacy_daemon() {
    launchctl kickstart "gui/$(id -u)/com.example.commander"
}
