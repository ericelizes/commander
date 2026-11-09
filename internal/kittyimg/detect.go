package kittyimg

import (
	"fmt"
	"strings"
)

// Env reads an environment variable; Tmux runs a tmux command and returns its
// trimmed stdout ("" on error). Both are test seams.
type (
	Env  func(string) string
	Tmux func(args ...string) string
)

// Supported reports whether the outer terminal speaks kitty graphics.
// $CMDR_GRAPHICS=kitty forces it on, =off (or text/none) forces it off.
// Otherwise it looks for Ghostty/kitty in the environment, and inside tmux
// (which rewrites TERM and TERM_PROGRAM) asks tmux for the attached client's
// terminal and for the server's global environment.
func Supported(getenv Env, tmux Tmux) bool {
	switch strings.ToLower(getenv("CMDR_GRAPHICS")) {
	case "kitty", "on":
		return true
	case "off", "text", "none":
		return false
	}
	if isKittyTerm(getenv("TERM"), getenv("TERM_PROGRAM")) || getenv("KITTY_WINDOW_ID") != "" || getenv("GHOSTTY_RESOURCES_DIR") != "" {
		return true
	}
	if getenv("TMUX") == "" || tmux == nil {
		return false
	}
	if isKittyTerm(tmux("display-message", "-p", "#{client_termname}"), "") {
		return true
	}
	for _, name := range []string{"TERM_PROGRAM", "KITTY_WINDOW_ID", "GHOSTTY_RESOURCES_DIR"} {
		// "NAME=value", or "-NAME" when unset.
		_, val, ok := strings.Cut(tmux("show-environment", "-g", name), "=")
		if !ok || val == "" {
			continue
		}
		if name != "TERM_PROGRAM" || isKittyTerm("", val) {
			return true
		}
	}
	return false
}

func isKittyTerm(term, program string) bool {
	term, program = strings.ToLower(term), strings.ToLower(program)
	return strings.Contains(term, "ghostty") || strings.Contains(term, "kitty") ||
		program == "ghostty" || program == "kitty"
}

// Default cell size in pixels when neither the override nor tmux knows it.
const (
	DefaultCellW = 9
	DefaultCellH = 21
)

// CellSize is the pixel size of one terminal cell: $CMDR_CELL_PX ("9x21"),
// else tmux's view of the attached client, else DefaultCellW x DefaultCellH.
// (The `\e[16t` query would need a reply on stdin, which Bubble Tea owns.)
func CellSize(getenv Env, tmux Tmux) (w, h int) {
	if w, h, ok := parseWxH(getenv("CMDR_CELL_PX")); ok {
		return w, h
	}
	if getenv("TMUX") != "" && tmux != nil {
		if w, h, ok := parseWxH(tmux("display-message", "-p", "#{client_cell_width}x#{client_cell_height}")); ok {
			return w, h
		}
	}
	return DefaultCellW, DefaultCellH
}

func parseWxH(s string) (int, int, bool) {
	var w, h int
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}
