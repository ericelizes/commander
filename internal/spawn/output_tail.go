package spawn

import (
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/example-user/commander/internal/store"
)

// OutputTail is the last lines of a host run's pane output, for a Manager
// told its Operator died: control bytes stripped, each stream-json line cut
// to what it says (type and its text), at most lines lines of 200 runes.
func OutputTail(run store.AgentRun, projectPath string, lines int) string {
	f, err := os.Open(runOutputPath(run, projectPath))
	if err != nil {
		return ""
	}
	defer f.Close()
	const window = 16 << 10
	if fi, err := f.Stat(); err == nil && fi.Size() > window {
		_, _ = f.Seek(fi.Size()-window, io.SeekStart)
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	var out []string
	for _, l := range strings.Split(ansiRE.ReplaceAllString(string(raw), ""), "\n") {
		if l = outputLine(l); l != "" {
			out = append(out, l)
		}
	}
	if len(out) > lines {
		out = out[len(out)-lines:]
	}
	return strings.Join(out, "\n")
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07`)

// outputLine is one pane line as a person reads it: a stream-json event is
// its type (and subtype) and the first text it carries.
func outputLine(l string) string {
	l = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < ' ' && r != '\t' {
			return -1
		}
		return r
	}, l))
	if strings.HasPrefix(l, "{") {
		var e struct {
			Type, Subtype, Result, Error, Description string
			Message                                   struct{ Content []struct{ Type, Text string } }
		}
		if json.Unmarshal([]byte(l), &e) == nil && e.Type != "" {
			if e.Type == "tool_progress" {
				return "" // heartbeats of a running tool say nothing
			}
			what := e.Type
			if e.Subtype != "" {
				what += "/" + e.Subtype
			}
			text := e.Result + e.Error + e.Description
			for _, c := range e.Message.Content {
				if text == "" && c.Text != "" {
					text = c.Text
				}
			}
			if text = strings.Join(strings.Fields(text), " "); text != "" {
				what += ": " + text
			}
			l = what
		}
	}
	if r := []rune(l); len(r) > 200 {
		l = string(r[:199]) + "…"
	}
	return l
}
