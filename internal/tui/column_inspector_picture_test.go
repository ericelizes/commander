package tui

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"

	"github.com/example-user/commander/internal/inspector"
	"github.com/example-user/commander/internal/kittyimg"
)

// pictureColumn is an inspector on a kitty terminal inside tmux whose Chrome
// is a fake that writes a PNG; raw terminal writes are captured.
func pictureColumn(t *testing.T) (*InspectorColumn, *[]string) {
	t.Helper()
	c, st := newInspectorForTest(t, 40, 30)
	c.graphics, c.inTmux, c.imgHi = true, true, 1
	c.fetcher = inspector.Fetcher{
		Getenv:  func(string) string { return "" },
		Browser: inspector.BrowserConfig{Enabled: true, Dir: t.TempDir(), Chrome: "/fake/chrome", CellW: 9, CellH: 21},
		Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
			for _, a := range args {
				if p, ok := strings.CutPrefix(a, "--screenshot="); ok {
					return nil, os.WriteFile(p, []byte("PNG"), 0o644)
				}
			}
			return nil, errors.New("unexpected exec " + name)
		},
	}
	var raw []string
	orig := inspectorWriteRaw
	inspectorWriteRaw = func(s string) error { raw = append(raw, s); return nil }
	t.Cleanup(func() { inspectorWriteRaw = orig })
	addTabs(t, st, "https://example.com/page")
	drive(t, c, c.loadCmd()())
	return c, &raw
}

func TestInspector_WebTabIsAKittyPicture(t *testing.T) {
	c, raw := pictureColumn(t)
	card := c.cards[c.activeTab().ID]
	if card.Image == "" || card.ImageCols != 36 || card.ImageRows != 2*(c.height-8) {
		t.Fatalf("card = %+v (width %d height %d)", card, c.width, c.height)
	}
	if len(*raw) != 1 || !strings.HasPrefix((*raw)[0], "\x1bPtmux;\x1b\x1b_Ga=T,U=1,f=100,t=d,i=16777218,c=36,") {
		t.Fatalf("transmit = %q", *raw)
	}
	view := c.innerView()
	assertFits(t, view, c.width, c.height)
	if !strings.Contains(view, string(kitty.Placeholder)) || !strings.Contains(ansi.Strip(view), inspector.BrowserLoginHint) {
		t.Fatalf("no picture in view:\n%s", ansi.Strip(view))
	}
	firstRow := func() rune { // row diacritic of the first placeholder cell
		v := c.innerView()
		i := strings.IndexRune(v, kitty.Placeholder)
		return []rune(v[i:])[1]
	}
	if firstRow() != kitty.Diacritic(0) {
		t.Error("picture must start at row 0")
	}
	drive(t, c, key("j"))
	drive(t, c, key("j"))
	if firstRow() != kitty.Diacritic(2) {
		t.Errorf("j j should crop from image row 2, got %U", firstRow())
	}
	drive(t, c, key("G"))
	if c.imgOff+c.vp.Height != card.ImageRows {
		t.Errorf("G: off %d + body %d != rows %d", c.imgOff, c.vp.Height, card.ImageRows)
	}
	// Re-rendering never re-uploads; a refresh (new fetch) does.
	c.innerView()
	if len(*raw) != 1 {
		t.Errorf("re-render re-sent the picture: %d sends", len(*raw))
	}
	drive(t, c, key("r"))
	if len(*raw) != 2 {
		t.Errorf("r should reshoot and re-send, got %d sends", len(*raw))
	}
}

func TestInspector_WebTabWithoutGraphicsIsLinkCardWithHint(t *testing.T) {
	c, raw := pictureColumn(t)
	c.graphics = false
	c.fetcher.Browser.Enabled = false
	drive(t, c, key("r"))
	v := ansi.Strip(c.innerView())
	if strings.Contains(c.innerView(), string(kitty.Placeholder)) || len(*raw) > 1 {
		t.Fatal("no picture without kitty graphics")
	}
	for _, want := range []string{"Ghostty", "Press o to open"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
}

func TestInspector_BOpensCarbonylOrHints(t *testing.T) {
	c, _ := pictureColumn(t)
	origLook, origPopup := inspectorLookPath, inspectorPopup
	t.Cleanup(func() { inspectorLookPath, inspectorPopup = origLook, origPopup })

	inspectorLookPath = func(string) (string, error) { return "", errors.New("not found") }
	drive(t, c, key("B"))
	if !strings.Contains(c.status, "install carbonyl") {
		t.Errorf("status = %q", c.status)
	}

	var ran string
	inspectorLookPath = func(string) (string, error) { return "/bin/carbonyl", nil }
	inspectorPopup = func(cmd string) error { ran = cmd; return nil }
	drive(t, c, key("B"))
	if ran != "'/bin/carbonyl' 'https://example.com/page'" {
		t.Errorf("popup ran %q", ran)
	}
}

func TestInspector_ImageIDsDifferPerTab(t *testing.T) {
	c, _ := pictureColumn(t)
	if c.imageID(1) == c.imageID(2) || c.imageID(1) != kittyimg.ID(1, 2) {
		t.Errorf("ids: %d %d", c.imageID(1), c.imageID(2))
	}
}
