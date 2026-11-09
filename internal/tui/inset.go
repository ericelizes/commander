package tui

import "strings"

// Inner padding of our own panels: panels stay full-bleed (their background
// fills to the pane edges) but text sits this far inside them.
const (
	panelPadX = 2
	panelPadY = 1
)

// insetView shifts a view panelPadX cells right and panelPadY rows down. The
// pane's background supplies the colour of the padding.
func insetView(view string) string {
	pad := strings.Repeat(" ", panelPadX)
	lines := strings.Split(view, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = pad + l
		}
	}
	return strings.Repeat("\n", panelPadY) + strings.Join(lines, "\n")
}

// innerSize is the content area inside the panel padding (top and bottom rows too).
func innerSize(w, h int) (int, int) {
	return max(0, w-2*panelPadX), max(0, h-2*panelPadY)
}

// frameKeeper stops a column from ever painting blank: when a render comes out
// empty (a size of 0 mid-resize, data missing during a daemon restart), the last
// good frame is shown instead.
type frameKeeper struct{ last string }

func (f *frameKeeper) keep(view string) string {
	if strings.TrimSpace(view) == "" {
		return f.last
	}
	f.last = view
	return view
}
