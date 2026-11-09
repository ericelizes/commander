package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// Already-installed jobs keep working before the next explicit installation.
const legacyDaemonLabel = "com.example.commander"

func (h *appHost) cleanupLegacyUI() {
	const label = "com.example.commander-ui"
	dest := filepath.Join(h.home, "Library", "LaunchAgents", label+".plist")
	if _, err := os.Stat(dest); err != nil {
		return
	}
	_ = h.run("", "launchctl", "bootout", fmt.Sprintf("gui/%d/%s", h.uid, label))
	_ = os.Remove(dest)
}
