package kittyimg

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestPlaceholders_Golden(t *testing.T) {
	// id 7, no high byte: fg 38;5;7, then U+10EEEE + row diacritic + col diacritic.
	got := Placeholders(ID(0, 7), 1, 2, 2)
	want := []string{
		"\x1b[38;5;7m\U0010EEEE\u030D\u0305\U0010EEEE\u030D\u030D\x1b[39m",
		"\x1b[38;5;7m\U0010EEEE\u030E\u0305\U0010EEEE\u030E\u030D\x1b[39m",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d lines", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
}

func TestPlaceholders_HighByteIsThirdDiacritic(t *testing.T) {
	got := Placeholders(ID(2, 9), 0, 1, 1)[0]
	want := "\x1b[38;5;9m\U0010EEEE\u0305\u0305\u030E\x1b[39m" // diacritic #2 is U+030E
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestTransmit_GoldenDirect(t *testing.T) {
	png := []byte("PNGDATA")
	b64 := base64.StdEncoding.EncodeToString(png)
	got := Transmit(ID(0, 7), png, 40, 30, false)
	want := "\x1b_Ga=T,U=1,f=100,t=d,i=7,c=40,r=30,q=2,m=0;" + b64 + "\x1b\\"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestTransmit_TmuxPassthroughDoublesEscAndChunks(t *testing.T) {
	png := make([]byte, 4000) // 5336 base64 bytes -> two chunks
	got := Transmit(ID(0, 7), png, 4, 3, true)
	chunks := strings.Split(got, "\x1b\\\x1bPtmux;")
	if len(chunks) != 2 {
		t.Fatalf("want 2 passthrough chunks, got %d: %q", len(chunks), got[:80])
	}
	if !strings.HasPrefix(got, "\x1bPtmux;\x1b\x1b_Ga=T,U=1,f=100,t=d,i=7,c=4,r=3,q=2,m=1;") {
		t.Errorf("first chunk = %q", got[:60])
	}
	if !strings.Contains(got, "\x1bPtmux;\x1b\x1b_Gq=2,m=0;") {
		t.Error("last chunk must carry m=0")
	}
	if !strings.HasSuffix(got, "\x1b\x1b\\\x1b\\") {
		t.Errorf("must end with the doubled inner ST then the DCS ST: %q", got[len(got)-8:])
	}
}

func TestTransmitFile_Golden(t *testing.T) {
	got := TransmitFile(ID(0, 3), "/tmp/a.png", 10, 5, true)
	inner := "\x1b\x1b_Ga=T,U=1,f=100,t=f,i=3,c=10,r=5,q=2;" + base64.StdEncoding.EncodeToString([]byte("/tmp/a.png")) + "\x1b\x1b\\"
	if got != "\x1bPtmux;"+inner+"\x1b\\" {
		t.Errorf("got %q", got)
	}
}

func TestDelete(t *testing.T) {
	if got := Delete(ID(0, 3), false); got != "\x1b_Ga=d,d=I,i=3,q=2\x1b\\" {
		t.Errorf("got %q", got)
	}
}

func envOf(m map[string]string) Env { return func(k string) string { return m[k] } }

func TestSupported(t *testing.T) {
	noTmux := Tmux(func(...string) string { return "" })
	cases := []struct {
		name string
		env  map[string]string
		tmux Tmux
		want bool
	}{
		{"plain xterm", map[string]string{"TERM": "xterm-256color"}, noTmux, false},
		{"ghostty", map[string]string{"TERM_PROGRAM": "ghostty"}, noTmux, true},
		{"kitty", map[string]string{"KITTY_WINDOW_ID": "1"}, noTmux, true},
		{"xterm-ghostty", map[string]string{"TERM": "xterm-ghostty"}, noTmux, true},
		{"forced off", map[string]string{"TERM_PROGRAM": "ghostty", "CMDR_GRAPHICS": "off"}, noTmux, false},
		{"forced on", map[string]string{"CMDR_GRAPHICS": "kitty"}, noTmux, true},
		{"tmux client is ghostty", map[string]string{"TMUX": "/s", "TERM_PROGRAM": "tmux"}, func(args ...string) string {
			if args[0] == "display-message" {
				return "xterm-ghostty"
			}
			return ""
		}, true},
		{"tmux global env", map[string]string{"TMUX": "/s", "TERM_PROGRAM": "tmux"}, func(args ...string) string {
			if args[0] == "show-environment" && args[2] == "TERM_PROGRAM" {
				return "TERM_PROGRAM=ghostty"
			}
			return "-" + args[len(args)-1]
		}, true},
		{"tmux in iTerm", map[string]string{"TMUX": "/s", "TERM_PROGRAM": "tmux"}, func(args ...string) string {
			if args[0] == "display-message" {
				return "xterm-256color"
			}
			if args[2] == "TERM_PROGRAM" {
				return "TERM_PROGRAM=iTerm.app"
			}
			return "-" + args[2]
		}, false},
	}
	for _, tc := range cases {
		if got := Supported(envOf(tc.env), tc.tmux); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestCellSize(t *testing.T) {
	tm := Tmux(func(...string) string { return "18x42" })
	if w, h := CellSize(envOf(map[string]string{"CMDR_CELL_PX": "10x20", "TMUX": "x"}), tm); w != 10 || h != 20 {
		t.Errorf("override: %dx%d", w, h)
	}
	if w, h := CellSize(envOf(map[string]string{"TMUX": "x"}), tm); w != 18 || h != 42 {
		t.Errorf("tmux: %dx%d", w, h)
	}
	if w, h := CellSize(envOf(map[string]string{"TMUX": "x"}), func(...string) string { return "0x0" }); w != DefaultCellW || h != DefaultCellH {
		t.Errorf("default: %dx%d", w, h)
	}
}
