package operatorguard

import (
	"fmt"
	"strings"
)

// The read-only place guard does not parse shell. It accepts only command
// text in which the shell (bash or zsh, the macOS default) can expand
// nothing: every word is plain characters or quoted text, so the words the
// guard judges are exactly the argv the shell passes on. Braces, globs,
// variables, substitutions, escapes, comments, subshells, a tilde anywhere
// but the start of a word, and shell keywords never pass; neither do here
// documents whose body the shell would expand. Two rounds of judging shell
// text after the shell's own rules each leaked (cd, ~name, then brace and
// tilde expansion), so the guard no longer tries to follow those rules: what
// it cannot read literally it refuses.

// placeSeg is one simple command of a line: its literal words, the targets
// it writes (> and >>) and the files it reads (<).
type placeSeg struct {
	Words []string
	Out   []string
	In    []string
}

// plainChar is what may stand unquoted in a word.
func plainChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("_./:,+@%=^~-", c) >= 0
}

// opChar ends a word.
func opChar(c byte) bool { return strings.IndexByte(" \t\n;|&<>", c) >= 0 }

// refusedChar names why an unquoted character is refused.
func refusedChar(c byte) string {
	switch c {
	case '$':
		return "a variable or substitution ($)"
	case '`':
		return "a command substitution (`)"
	case '{', '}':
		return "brace expansion or a group ({ })"
	case '*', '?', '[', ']':
		return fmt.Sprintf("a glob (%c): quote it, or use the Glob tool", c)
	case '\\':
		return "an escape (\\): quote the text instead"
	case '#':
		return "a comment (#)"
	case '!':
		return "history or negation (!)"
	case '(', ')':
		return "a subshell, function or glob qualifier (( ))"
	case '\r':
		return "a carriage return"
	}
	if c >= 0x80 {
		return "a non-ASCII character outside quotes"
	}
	return fmt.Sprintf("a character the guard does not allow unquoted (%q)", c)
}

// placeKeywords are shell reserved words (bash and zsh): in command position
// they open loops, tests and functions whose variables the guard cannot see.
var placeKeywords = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "fi": true, "for": true, "while": true, "until": true,
	"do": true, "done": true, "case": true, "esac": true, "function": true, "select": true, "time": true,
	"coproc": true, "in": true, "repeat": true, "foreach": true, "end": true, "nocorrect": true, "noglob": true,
}

type heredoc struct {
	delim string
	strip bool // <<-: leading tabs are stripped from body lines
}

// lexPlace splits line into simple commands of literal words. why is the
// refusal when the line holds anything the shell would expand or the guard
// does not know.
func lexPlace(line string) (segs []placeSeg, why string) {
	var cur placeSeg
	var pending []heredoc
	flush := func() {
		if len(cur.Words) > 0 || len(cur.Out) > 0 || len(cur.In) > 0 {
			segs = append(segs, cur)
		}
		cur = placeSeg{}
	}
	n := len(line)
	i := 0
	// word reads one word at i (after blanks); "" with ok=false when no word starts there.
	word := func() (string, bool, string) {
		for i < n && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i >= n || opChar(line[i]) {
			return "", false, ""
		}
		var b strings.Builder
		start := i
		for i < n && !opChar(line[i]) {
			c := line[i]
			switch {
			case (c == '\'' || c == '"') && i == start && i+1 < n && line[i+1] == '~':
				// The shell keeps a quoted ~ as text; the guard would read a home.
				return "", false, "a quoted ~ at the start of a word"
			case c == '\'':
				j := strings.IndexByte(line[i+1:], '\'')
				if j < 0 {
					return "", false, "an unterminated quote"
				}
				b.WriteString(line[i+1 : i+1+j])
				i += j + 2
			case c == '"':
				j := strings.IndexByte(line[i+1:], '"')
				if j < 0 {
					return "", false, "an unterminated quote"
				}
				body := line[i+1 : i+1+j]
				if k := strings.IndexAny(body, "$`\\!"); k >= 0 {
					return "", false, fmt.Sprintf("%q inside double quotes expands: use single quotes", body[k])
				}
				b.WriteString(body)
				i += j + 2
			case !plainChar(c):
				return "", false, refusedChar(c)
			case c == '~':
				// Only ~ alone or ~/ at the start of a word (expanded to the
				// home by the guard as by the shell); never ~name, and never
				// after = or : where bash expands it in assignment-like words.
				if i == start {
					if i+1 < n && !opChar(line[i+1]) && line[i+1] != '/' {
						return "", false, "a ~ that is not ~ or ~/ (another user's home or a named directory)"
					}
				} else if p := line[i-1]; p == '=' || p == ':' {
					return "", false, "a ~ after = or : (the shell may expand it)"
				}
				b.WriteByte(c)
				i++
			case i == start && (c == '=' || c == '^'):
				return "", false, fmt.Sprintf("a word starting with %c (zsh expands it)", c)
			default:
				b.WriteByte(c)
				i++
			}
		}
		return b.String(), true, ""
	}
	for i < n {
		c := line[i]
		switch {
		case c == ' ' || c == '\t':
			i++
		case c == '\n':
			i++
			flush()
			for _, h := range pending {
				end := -1
				for end < 0 {
					if i >= n {
						return nil, "a here document with no end line (" + h.delim + ")"
					}
					j := strings.IndexByte(line[i:], '\n')
					l := line[i:]
					if j >= 0 {
						l = line[i : i+j]
					}
					if h.strip {
						l = strings.TrimLeft(l, "\t")
					}
					if l == h.delim {
						end = i
					}
					if j < 0 {
						i = n
					} else {
						i += j + 1
					}
				}
			}
			pending = nil
		case c == ';':
			if i+1 < n && line[i+1] == ';' {
				return nil, "a case clause (;;)"
			}
			i++
			flush()
		case c == '|':
			if i+1 < n && line[i+1] == '&' {
				return nil, "a pipe of both streams (|&)"
			}
			if i+1 < n && line[i+1] == '|' {
				i++
			}
			i++
			flush()
		case c == '&':
			if i+1 < n && line[i+1] == '&' {
				i += 2
				flush()
				continue
			}
			return nil, "a background job or a redirect of both streams (&)"
		case c == '>' || c == '<' || (c >= '0' && c <= '9' && redirectAfterDigits(line, i)):
			for i < n && line[i] >= '0' && line[i] <= '9' {
				i++
			}
			if line[i] == '>' {
				i++
				if i < n && line[i] == '>' {
					i++
				}
				if i < n && line[i] == '&' {
					// >&2, 2>&1, >&-: a copy of a descriptor, no file.
					i++
					j := i
					for i < n && (line[i] >= '0' && line[i] <= '9' || line[i] == '-') {
						i++
					}
					if i == j || i < n && !opChar(line[i]) {
						return nil, "a >& redirect to a file (name the file with >)"
					}
					continue
				}
				if i < n && (line[i] == '|' || line[i] == '!' || line[i] == '(') {
					return nil, "a forced or process redirect (>| >! >( )"
				}
				w, ok, why := word()
				if why != "" {
					return nil, why
				}
				if !ok {
					return nil, "a redirect with no target"
				}
				cur.Out = append(cur.Out, w)
				continue
			}
			// <
			i++
			if i < n && line[i] == '<' {
				i++
				if i < n && line[i] == '<' {
					return nil, "a here string (<<<)"
				}
				strip := false
				if i < n && line[i] == '-' {
					strip = true
					i++
				}
				for i < n && (line[i] == ' ' || line[i] == '\t') {
					i++
				}
				if i >= n || line[i] != '\'' && line[i] != '"' {
					return nil, "a here document with an unquoted end word (the shell expands its body): write <<'EOF'"
				}
				w, ok, why := word()
				if why != "" {
					return nil, why
				}
				if !ok || w == "" {
					return nil, "a here document with no end word"
				}
				pending = append(pending, heredoc{delim: w, strip: strip})
				continue
			}
			if i < n && (line[i] == '>' || line[i] == '&' || line[i] == '(') {
				return nil, "a read-write, copied or process redirect (<> <& <( )"
			}
			if i >= n || line[i] != ' ' && line[i] != '\t' {
				// zsh reads <1-10> as a numeric glob: a file read by < is
				// written with a blank after it.
				return nil, "a < with no blank after it (write < file)"
			}
			w, ok, why := word()
			if why != "" {
				return nil, why
			}
			if !ok {
				return nil, "a redirect with no source"
			}
			cur.In = append(cur.In, w)
		default:
			w, ok, why := word()
			if why != "" {
				return nil, why
			}
			if !ok {
				return nil, refusedChar(line[i])
			}
			if len(cur.Words) == 0 && placeKeywords[w] {
				return nil, "a shell keyword (" + w + ")"
			}
			cur.Words = append(cur.Words, w)
		}
	}
	if len(pending) > 0 {
		return nil, "a here document with no body"
	}
	flush()
	return segs, ""
}

// redirectAfterDigits reports a descriptor number right before > or < (2>x).
func redirectAfterDigits(line string, i int) bool {
	if i > 0 && !opChar(line[i-1]) {
		return false
	}
	j := i
	for j < len(line) && line[j] >= '0' && line[j] <= '9' {
		j++
	}
	return j < len(line) && (line[j] == '>' || line[j] == '<')
}
