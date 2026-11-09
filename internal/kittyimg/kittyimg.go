// Package kittyimg builds the kitty graphics protocol bytes the inspector uses
// to show a picture in the cell grid: a transmit sequence that stores a PNG in
// the terminal with a *virtual* placement, plus rows of Unicode placeholder
// cells (U+10EEEE + row/column diacritics, image id in the foreground colour)
// that the terminal replaces with slices of that image. Because the picture
// lives in ordinary text cells it scrolls, clears and redraws with the rest of
// the TUI, and tmux keeps it in its grid like any other character.
//
// Inside tmux the transmit sequence is wrapped in a DCS passthrough (tmux needs
// `allow-passthrough`); the placeholder cells are plain text and need nothing.
//
// It is pure bytes: no terminal I/O, no Bubble Tea.
package kittyimg

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
)

// chunkSize is the base64 payload per escape (the protocol's 4096 limit).
const chunkSize = 4096

// ID is an image id whose low byte rides in a 256-colour foreground (38;5;lo)
// and whose high byte rides in the third placeholder diacritic. It avoids
// 24-bit colour, which tmux may downsample and so corrupt the id. lo and hi
// are 1..255.
func ID(hi, lo uint8) uint32 {
	return uint32(hi)<<24 | uint32(lo)
}

// Transmit returns the escapes that upload png as image id with a virtual
// placement of cols x rows cells (U=1), quietly (q=2: no replies on stdin).
// tmux wraps every chunk in a DCS passthrough.
func Transmit(id uint32, png []byte, cols, rows int, tmux bool) string {
	data := base64.StdEncoding.EncodeToString(png)
	var b strings.Builder
	for first := true; first || data != ""; first = false {
		n := min(chunkSize, len(data))
		chunk := data[:n]
		data = data[n:]
		more := 0
		if data != "" {
			more = 1
		}
		var opts []string
		if first {
			opts = []string{"a=T", "U=1", "f=100", "t=d", fmt.Sprintf("i=%d", id), fmt.Sprintf("c=%d", cols), fmt.Sprintf("r=%d", rows), "q=2"}
		} else {
			opts = []string{"q=2"}
		}
		opts = append(opts, fmt.Sprintf("m=%d", more))
		b.WriteString(Wrap(ansi.KittyGraphics([]byte(chunk), opts...), tmux))
	}
	return b.String()
}

// TransmitFile is Transmit with the terminal reading the PNG from path itself
// (t=f): a few bytes on the wire instead of the whole image. Local terminals only.
func TransmitFile(id uint32, path string, cols, rows int, tmux bool) string {
	payload := base64.StdEncoding.EncodeToString([]byte(path))
	seq := ansi.KittyGraphics([]byte(payload), "a=T", "U=1", "f=100", "t=f",
		fmt.Sprintf("i=%d", id), fmt.Sprintf("c=%d", cols), fmt.Sprintf("r=%d", rows), "q=2")
	return Wrap(seq, tmux)
}

// Delete frees image id (and its placements) in the terminal.
func Delete(id uint32, tmux bool) string {
	return Wrap(ansi.KittyGraphics(nil, "a=d", "d=I", fmt.Sprintf("i=%d", id), "q=2"), tmux)
}

// Wrap puts seq in a tmux DCS passthrough (ESCs doubled) when tmux is set.
func Wrap(seq string, tmux bool) string {
	if !tmux {
		return seq
	}
	return ansi.TmuxPassthrough(seq)
}

// Placeholders returns rows lines of cols placeholder cells showing image rows
// [firstRow, firstRow+rows) of image id. Every cell carries its row and column
// diacritics (and the id's high byte) so tmux can redraw any cell on its own.
func Placeholders(id uint32, firstRow, rows, cols int) []string {
	lo, hi := id&0xff, int(id>>24)
	open := fmt.Sprintf("\x1b[38;5;%dm", lo)
	lines := make([]string, 0, rows)
	for r := firstRow; r < firstRow+rows; r++ {
		var b strings.Builder
		b.WriteString(open)
		for c := range cols {
			b.WriteRune(kitty.Placeholder)
			b.WriteRune(kitty.Diacritic(r))
			b.WriteRune(kitty.Diacritic(c))
			if hi != 0 {
				b.WriteRune(kitty.Diacritic(hi))
			}
		}
		b.WriteString("\x1b[39m")
		lines = append(lines, b.String())
	}
	return lines
}

// MaxCells is the largest row or column index a placeholder can encode.
const MaxCells = 297
