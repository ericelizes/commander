package chat

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// SeenExchanges is how many exchanges SeenDigest hands an agent.
const SeenExchanges = 12

const (
	seenUserRunes  = 800
	seenReplyRunes = 1500
	seenMailLines  = 4
	seenMailRunes  = 200
	// seenTailStart is the first slice of the log read from its end; it
	// doubles until it holds enough exchanges or is the whole log.
	seenTailStart = 2 << 20
)

// seenTurn is one exchange as the app showed it: what started it (the user's
// messages, or agent mail) and the reply that ended it.
type seenTurn struct {
	at       time.Time
	user     []string
	mail     []string
	reply    string
	lastText string
	done     bool
}

// SeenDigest is the last n exchanges between the user and role as the app
// showed them, read from <home>/chat/<role>.jsonl: the user's messages, the
// agent mail that started a turn, and the reply that ended each turn (no tool
// calls, thinking or hook context), newest last. An agent's own memory of the
// conversation is lost to a fresh session or summarized by compaction; this
// record is what the user actually saw. "" when the log has no exchanges.
func SeenDigest(home, role string, n int) string {
	path := filepath.Join(home, "chat", role+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	var turns []seenTurn
	for size := int64(seenTailStart); ; size *= 2 {
		whole := size >= fi.Size()
		if whole {
			size = fi.Size()
		}
		buf := make([]byte, size)
		if _, err := f.ReadAt(buf, fi.Size()-size); err != nil && err != io.EOF {
			return ""
		}
		if !whole {
			// The first line of a slice is cut, and so may be its first exchange.
			if i := bytes.IndexByte(buf, '\n'); i >= 0 {
				buf = buf[i+1:]
			}
		}
		turns = seenTurns(buf)
		if whole {
			break
		}
		if len(turns) > n {
			turns = turns[1:]
			break
		}
	}
	if len(turns) > n {
		turns = turns[len(turns)-n:]
	}
	return renderSeen(turns)
}

// seenTurns groups the top-level conversation events of a log into exchanges.
func seenTurns(raw []byte) []seenTurn {
	var turns []seenTurn
	cur := -1
	open := func(at time.Time) {
		turns = append(turns, seenTurn{at: at})
		cur = len(turns) - 1
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.ParentToolUseID != "" {
			continue
		}
		switch e.Kind {
		case KindUser:
			var text string
			switch e.Origin {
			case "user", "transcript":
				text = strings.TrimSpace(userScreen.ReplaceAllString(e.Text, ""))
			case "mail":
				text = strings.TrimSpace(e.Text)
			}
			// The CLI's own echoes (a blocked Stop's reason, an interrupt) are not messages.
			if text == "" || strings.HasPrefix(text, "Stop hook feedback:") || strings.HasPrefix(text, "[Request interrupted") {
				continue
			}
			// A message after a reply starts the next exchange, also in
			// hydrated history, which has no turn results.
			if cur < 0 || turns[cur].done || turns[cur].lastText != "" {
				if cur >= 0 && !turns[cur].done {
					turns[cur].reply, turns[cur].done = turns[cur].lastText, true
				}
				open(e.Time)
			}
			if e.Origin == "mail" {
				turns[cur].mail = append(turns[cur].mail, text)
			} else {
				turns[cur].user = append(turns[cur].user, text)
			}
		case KindText:
			if strings.TrimSpace(e.Text) == "" {
				continue
			}
			if cur < 0 || turns[cur].done {
				open(e.Time)
			}
			turns[cur].lastText = e.Text
		case KindResult:
			if cur < 0 || turns[cur].done {
				continue
			}
			reply := turns[cur].lastText
			if e.Result != nil && strings.TrimSpace(e.Result.Text) != "" {
				reply = e.Result.Text
			}
			turns[cur].reply, turns[cur].done = reply, true
		}
	}
	if cur >= 0 && !turns[cur].done {
		turns[cur].reply = turns[cur].lastText
	}
	return turns
}

// userScreen is the app's <user-screen> block, which rides along with a
// message to the agent; the user sees only what they typed (app screen.ts).
var userScreen = regexp.MustCompile(`^\s*<user-screen>[\s\S]*?</user-screen>\s*`)

func renderSeen(turns []seenTurn) string {
	if len(turns) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Conversation so far as the user saw it: the last %d exchanges between the user and you, oldest first, newest last, "+
		"from the Commander app's chat record. This is what the user was actually shown, and it is the authority on what they have and have not seen: "+
		"your own memory of the conversation may be missing (a fresh session) or summarized (compaction). Replies are your final reply of each turn, trimmed.\n", len(turns))
	for _, t := range turns {
		fmt.Fprintf(&b, "\n[%s]\n", t.at.UTC().Format("Jan 2 15:04 UTC"))
		for _, u := range t.user {
			fmt.Fprintf(&b, "User: %s\n", clip(u, seenUserRunes))
		}
		if len(t.mail) > 0 {
			b.WriteString("Agent mail reached you (the user did not write it):\n")
			lines := mailLines(t.mail)
			for i, l := range lines {
				if i == seenMailLines {
					fmt.Fprintf(&b, "  … %d more\n", len(lines)-i)
					break
				}
				fmt.Fprintf(&b, "  %s\n", clip(l, seenMailRunes))
			}
		}
		switch {
		case t.reply != "":
			fmt.Fprintf(&b, "You replied: %s\n", clip(t.reply, seenReplyRunes))
		case t.done:
			b.WriteString("You replied: (nothing; the turn ended without a reply)\n")
		default:
			b.WriteString("You replied: (no reply yet)\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func mailLines(mails []string) []string {
	var out []string
	for _, m := range mails {
		for _, l := range strings.Split(m, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, l)
			}
		}
	}
	return out
}

// clip trims s to max runes, marking the cut.
func clip(s string, max int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > max {
		return strings.TrimRight(string(r[:max-1]), " \n") + "… (trimmed)"
	}
	return s
}
