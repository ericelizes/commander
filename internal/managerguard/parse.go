package managerguard

import "strings"

// segment is one simple command of a shell line: its words, the targets of
// its output redirects, and the bodies of $(...) / `...` substitutions.
type segment struct {
	words      []string
	redirects  []string
	subs       []string
	piped      bool
	inputText  string
	hereString bool
}

// parse splits a shell line on ; && || | & and newlines (outside quotes),
// tokenizes each part, and separates heredoc data from executable shell bodies.
// It is a classifier's parser,
// not a shell: unusual syntax degrades to extra words, never a panic.
func parse(line string) []segment {
	var (
		segs       []segment
		cur        segment
		word       strings.Builder
		haveWord   bool
		quote      rune
		redirect   bool // the next word is a redirect target
		hereSource bool
		heredocs   []heredoc
	)
	flushWord := func() {
		if !haveWord {
			return
		}
		w := word.String()
		word.Reset()
		haveWord = false
		if hereSource {
			cur.inputText += w
			hereSource = false
			return
		}
		if redirect {
			cur.redirects = append(cur.redirects, w)
			redirect = false
			return
		}
		cur.words = append(cur.words, w)
	}
	flushSeg := func() {
		flushWord()
		if cur.hereString && shellStdin(unwrap(stripGroup(cur.words), false)) {
			cur.subs = append(cur.subs, cur.inputText)
		}
		if len(cur.words) > 0 || len(cur.redirects) > 0 || len(cur.subs) > 0 || len(heredocs) > 0 && heredocs[len(heredocs)-1].owner == len(segs) {
			segs = append(segs, cur)
		}
		cur = segment{}
	}
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		if quote != 0 {
			if c == quote {
				quote = 0
				continue
			}
			// as in a shell, any other backslash inside double quotes stays in the word
			if c == '\\' && quote == '"' && i+1 < len(rs) && strings.ContainsRune("$`\"\\\n", rs[i+1]) {
				i++
				c = rs[i]
				if c == '$' || c == '`' { // an escaped $ is literal data, not a substitution
					word.WriteRune(c)
					continue
				}
			}
			if quote == '\'' || c != '$' && c != '`' {
				word.WriteRune(c)
				continue
			}
			// substitutions run inside double quotes
			if end := subEnd(rs, i); end > i {
				cur.subs = append(cur.subs, subBody(string(rs[i:end])))
				word.WriteString(string(rs[i:end]))
				i = end - 1
				haveWord = true
				continue
			}
			word.WriteRune(c)
			continue
		}
		switch c {
		case '\'', '"':
			quote, haveWord = c, true
		case '\\':
			if i+1 < len(rs) {
				i++
				word.WriteRune(rs[i])
				haveWord = true
			}
		case '$', '`':
			if end := subEnd(rs, i); end > i {
				cur.subs = append(cur.subs, subBody(string(rs[i:end])))
				word.WriteString(string(rs[i:end]))
				i = end - 1
				haveWord = true
				continue
			}
			word.WriteRune(c)
			haveWord = true
		case ' ', '\t':
			flushWord()
		case '\n':
			flushSeg()
			for len(heredocs) > 0 { // skip the heredoc body
				doc := heredocs[0]
				var body strings.Builder
				heredocs = heredocs[1:]
				for i+1 < len(rs) {
					j := i + 1
					for j < len(rs) && rs[j] != '\n' {
						j++
					}
					bodyLine := string(rs[i+1 : j])
					lineText := strings.TrimSpace(bodyLine)
					i = j
					if lineText == doc.delim {
						break
					}
					body.WriteString(bodyLine)
					body.WriteRune('\n')
				}
				text := body.String()
				if !doc.quoted {
					segs[doc.owner].subs = append(segs[doc.owner].subs, heredocSubstitutions(text)...)
				}
				for target := doc.owner; target < len(segs); target++ {
					segs[target].inputText += text
					words := unwrap(stripGroup(segs[target].words), false)
					if shellStdin(words) {
						segs[target].subs = append(segs[target].subs, text)
					}
					if !segs[target].piped {
						break
					}
				}
			}
		case ';', '|', '&':
			// "2>&1", ">&2", "&>" are redirects, not separators.
			if c == '&' && (i > 0 && rs[i-1] == '>' || i+1 < len(rs) && rs[i+1] == '>') {
				if i+1 < len(rs) && rs[i+1] == '>' { // &> file
					i++
					flushWord()
					redirect = true
				}
				continue
			}
			cur.piped = c == '|' && !(i+1 < len(rs) && rs[i+1] == '|')
			flushSeg()
			if i+1 < len(rs) && rs[i+1] == c {
				i++
			}
		case '>':
			// a leading fd digit ("2>") belongs to the redirect, not a word
			if haveWord && isDigits(word.String()) {
				word.Reset()
				haveWord = false
			} else {
				flushWord()
			}
			if i+1 < len(rs) && rs[i+1] == '>' {
				i++
			}
			if i+1 < len(rs) && rs[i+1] == '(' { // >(cmd): process substitution
				procSub(rs, &i, &cur, &word, &haveWord)
				continue
			}
			if i+1 < len(rs) && rs[i+1] == '&' {
				i++
				if i+1 < len(rs) && (isDigits(string(rs[i+1])) || rs[i+1] == '-') { // >&2, >&-: no file
					continue
				}
				// >&file and >& file write the file like >file
			}
			redirect = true
		case '<':
			if i+1 < len(rs) && rs[i+1] == '(' { // <(cmd): process substitution
				procSub(rs, &i, &cur, &word, &haveWord)
				continue
			}
			if i+1 < len(rs) && rs[i+1] == '<' {
				i++
				if i+1 < len(rs) && rs[i+1] == '<' { // here-string
					i++
					flushWord()
					cur.hereString = true
					hereSource = true
					continue
				}
				if i+1 < len(rs) && rs[i+1] == '-' {
					i++
				}
				flushWord()
				j := i + 1
				for j < len(rs) && (rs[j] == ' ' || rs[j] == '\t') {
					j++
				}
				k := j
				for k < len(rs) && !strings.ContainsRune(" \t\n;|&<>()", rs[k]) {
					k++
				}
				raw := string(rs[j:k])
				heredocs = append(heredocs, heredoc{strings.Trim(raw, `'"`), strings.ContainsAny(raw, `'"\`), len(segs)})
				i = k - 1
			}
		default:
			word.WriteRune(c)
			haveWord = true
		}
	}
	flushSeg()
	return segs
}

// procSub records the body of a <(...) or >(...) starting at rs[*i] as a
// substitution (it runs a command) and leaves a placeholder word for it.
func procSub(rs []rune, i *int, cur *segment, word *strings.Builder, haveWord *bool) {
	depth := 0
	for j := *i + 1; j < len(rs); j++ {
		switch rs[j] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 {
			cur.subs = append(cur.subs, string(rs[*i+2:j]))
			*i = j
			word.WriteString("<procsub>")
			*haveWord = true
			return
		}
	}
	cur.subs = append(cur.subs, string(rs[*i+2:])) // unterminated: judge the rest
	*i = len(rs)
	word.WriteString("<procsub>")
	*haveWord = true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// subEnd returns the index just past a $(...) or `...` starting at i, or i.
func subEnd(rs []rune, i int) int {
	if rs[i] == '`' {
		for j := i + 1; j < len(rs); j++ {
			if rs[j] == '`' {
				return j + 1
			}
		}
		return i
	}
	if i+1 < len(rs) && rs[i+1] == '(' {
		depth := 0
		for j := i + 1; j < len(rs); j++ {
			switch rs[j] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					return j + 1
				}
			}
		}
	}
	return i
}

func subBody(s string) string {
	s = strings.TrimPrefix(s, "$(")
	s = strings.TrimSuffix(s, ")")
	return strings.Trim(s, "`")
}

// Commands splits a shell line into the word lists of its simple commands,
// including those inside $(...) and backticks. For other guards that classify
// commands with the same parser.
func Commands(line string) [][]string {
	return commands(line, 0)
}

func commands(line string, depth int) [][]string {
	if depth > 4 {
		return nil
	}
	var out [][]string
	for _, seg := range parse(line) {
		if len(seg.words) > 0 {
			out = append(out, seg.words)
		}
		for _, sub := range seg.subs {
			out = append(out, commands(sub, depth+1)...)
		}
	}
	return out
}

type heredoc struct {
	delim  string
	quoted bool
	owner  int
}

func shellStdin(words []string) bool {
	if len(words) == 0 {
		return false
	}
	switch baseName(words[0]) {
	case "sh", "bash", "zsh", "dash":
	default:
		return false
	}
	stdin := false
	for i := 1; i < len(words); i++ {
		word := words[i]
		switch {
		case word == "--":
			return stdin || i+1 == len(words)
		case word == "-":
			stdin = true
		case word == "--help" || word == "--version":
			return false
		case word == "--rcfile" || word == "--init-file":
			i++
		case strings.HasPrefix(word, "--"):
			// Unknown long options are conservative: they may still read stdin.
		case strings.HasPrefix(word, "-") || strings.HasPrefix(word, "+"):
			flags := word[1:]
			if strings.Contains(flags, "c") {
				return false
			}
			if strings.HasPrefix(word, "-") && strings.Contains(flags, "s") {
				stdin = true
			}
			// -o/-O consume a shell option name, not a script filename.
			if strings.HasSuffix(flags, "o") || strings.HasSuffix(flags, "O") {
				i++
			}
		default:
			return stdin
		}
	}
	return true
}

func heredocSubstitutions(body string) []string {
	var out []string
	rs := []rune(body)
	for i := 0; i < len(rs); i++ {
		if rs[i] == '\\' {
			i++
			continue
		}
		if rs[i] != '$' && rs[i] != '`' {
			continue
		}
		if end := subEnd(rs, i); end > i {
			out = append(out, subBody(string(rs[i:end])))
			i = end - 1
		}
	}
	return out
}
