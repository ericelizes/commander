package boardturn

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Writer is cmdrd's side: it keeps each role's file current. Not safe for
// concurrent use (cmdrd builds one snapshot at a time).
type Writer struct {
	Home string

	loaded bool
	last   map[string]written
}

type written struct {
	version int
	sig     string
	text    string
	at      time.Time
}

// Write records boards (by role) at now and returns each role's version. A
// version moves only when what a line says changes, not its age; a file is
// rewritten when its text changes or Heartbeat has passed, so a fresh file
// means cmdrd is alive. Roles no longer on the board lose their file.
func (w *Writer) Write(boards map[string][]Entry, now time.Time) map[string]int {
	if !w.loaded {
		w.load()
	}
	versions := make(map[string]int, len(boards))
	for role, es := range boards {
		if !Role(role) {
			continue
		}
		prev := w.last[role]
		cur := prev
		if sig := signature(es); sig != prev.sig || prev.version == 0 {
			cur.version, cur.sig = prev.version+1, sig
		}
		f := File{Version: cur.version, At: now.Unix(), Entries: es}
		text := strings.Join(body(es), "\n")
		if cur.version != prev.version || text != prev.text || now.Sub(prev.at) >= Heartbeat {
			raw, _ := json.Marshal(f)
			txt := header(f) + "\n" + text + "\n"
			if writeAtomic(filePath(w.Home, role), raw) == nil && writeAtomic(filepath.Join(Dir(w.Home), role+".txt"), []byte(txt)) == nil {
				cur.text, cur.at = text, now
			}
		}
		w.last[role] = cur
		versions[role] = cur.version
	}
	for role := range w.last {
		if _, ok := boards[role]; !ok {
			_ = os.Remove(filePath(w.Home, role))
			_ = os.Remove(filepath.Join(Dir(w.Home), role+".txt"))
			delete(w.last, role)
		}
	}
	return versions
}

// load picks the versions up where the previous cmdrd left them, so a version
// an agent saw is never reused for a different board.
func (w *Writer) load() {
	w.loaded, w.last = true, map[string]written{}
	paths, _ := filepath.Glob(filepath.Join(Dir(w.Home), "*.json"))
	for _, p := range paths {
		role := strings.TrimSuffix(filepath.Base(p), ".json")
		raw, err := os.ReadFile(p)
		var f File
		if err != nil || json.Unmarshal(raw, &f) != nil || !Role(role) {
			continue
		}
		w.last[role] = written{version: f.Version, sig: signature(f.Entries)}
	}
}

func header(f File) string {
	return fmt.Sprintf("board v%d @ %s", f.Version, time.Unix(f.At, 0).Format(time.RFC3339))
}
