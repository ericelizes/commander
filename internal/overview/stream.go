package overview

import (
	"context"
	"encoding/json"
	"regexp"
	"unicode/utf8"
)

type progressKey struct{}

// WithProgress lets a reader report the recap it has written so far.
func WithProgress(ctx context.Context, f func(Recap)) context.Context {
	return context.WithValue(ctx, progressKey{}, f)
}

// ProgressOf is the progress callback a reader was given; it is never nil.
func ProgressOf(ctx context.Context) func(Recap) {
	if f := progressFrom(ctx); f != nil {
		return f
	}
	return func(Recap) {}
}

func progressFrom(ctx context.Context) func(Recap) {
	f, _ := ctx.Value(progressKey{}).(func(Recap))
	return f
}

// PartialRecap reads the sentences a half-written reader answer already holds.
// A sentence still being written counts too: its text so far, marked Partial,
// with no sources until its closing brace arrives.
func PartialRecap(buf string) Recap {
	type frame struct {
		open  byte
		start int
		key   string
	}
	var (
		r      Recap
		stack  []frame
		last   string
		inStr  bool
		esc    bool
		strAt  int
		claims = map[string]int{}
	)
	add := func(sec string, sent Sentence) {
		if sec == "summary" {
			r.Summary.Sentences = append(r.Summary.Sentences, sent)
			return
		}
		list := &r.Changes
		if sec == "next" {
			list = &r.Next
		} else if sec != "changes" {
			return
		}
		for len(*list) < claims[sec] {
			*list = append(*list, Claim{})
		}
		if n := claims[sec]; n > 0 {
			(*list)[n-1].Sentences = append((*list)[n-1].Sentences, sent)
		}
	}
	section := func() string {
		if len(stack) > 0 {
			return stack[0].key
		}
		return ""
	}
	for i := 0; i < len(buf); i++ {
		c := buf[i]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
				last = buf[strAt : i+1]
			}
			continue
		}
		switch c {
		case '"':
			inStr, strAt = true, i
		case ':':
			if len(stack) > 0 && stack[len(stack)-1].open == '{' {
				var k string
				_ = json.Unmarshal([]byte(last), &k)
				stack[len(stack)-1].key = k
			}
		case '{', '[':
			stack = append(stack, frame{open: c, start: i})
			if c == '{' && len(stack) == 3 && section() != "summary" {
				claims[section()]++
			}
		case '}', ']':
			if len(stack) == 0 {
				return r
			}
			f := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if c != '}' || len(stack) < 3 {
				continue
			}
			var s struct {
				Text      string          `json:"text"`
				Sources   *[]string       `json:"sources"`
				Sentences json.RawMessage `json:"sentences"`
			}
			if json.Unmarshal([]byte(buf[f.start:i+1]), &s) != nil || s.Text == "" || s.Sources == nil || s.Sentences != nil {
				continue
			}
			add(stack[0].key, Sentence{Text: s.Text, Sources: *s.Sources})
		}
	}
	// A sentence still open: show the text it has so far, character by character.
	if n := len(stack); n > 0 {
		if n > 1 && stack[n-1].open == '[' && stack[n-2].open == '{' {
			n-- // inside the sentence's own sources list
		}
		top := stack[n-1]
		sec := stack[0].key
		if top.open == '{' && ((sec == "summary" && n == 4) || ((sec == "changes" || sec == "next") && n == 5)) {
			if text := openText(buf[top.start:]); text != "" {
				add(sec, Sentence{Text: text, Sources: []string{}, Partial: true})
			}
		}
	}
	return r
}

var highSurrogate = regexp.MustCompile(`^[dD][89abAB][0-9a-fA-F]{2}$`)

var textKey = regexp.MustCompile(`"text"\s*:\s*"((?:[^"\\]|\\.)*)`)

// openText is the text value of a sentence object that may not be finished:
// everything after the opening quote, with escapes read and a half-written
// escape at the end left off.
func openText(obj string) string {
	m := textKey.FindStringSubmatch(obj)
	if m == nil {
		return ""
	}
	raw := m[1]
	// Drop a half-written escape at the end: a lone backslash or \uXXXX cut short.
	for cut := 1; cut <= 6 && cut <= len(raw); cut++ {
		i := len(raw) - cut
		if raw[i] != '\\' {
			continue
		}
		run := 0
		for j := i; j >= 0 && raw[j] == '\\'; j-- {
			run++
		}
		if run%2 == 1 && ((cut == 1) || (raw[i+1] == 'u' && cut < 6)) {
			raw = raw[:i]
		}
		break
	}
	// Hold back a high surrogate whose low half has not arrived, or it reads as U+FFFD.
	if n := len(raw); n >= 6 && raw[n-6] == '\\' && raw[n-5] == 'u' && highSurrogate.MatchString(raw[n-4:]) {
		run := 0
		for j := n - 6; j >= 0 && raw[j] == '\\'; j-- {
			run++
		}
		if run%2 == 1 {
			raw = raw[:n-6]
		}
	}
	// Raw UTF-8 cut mid-character waits for the rest of it too.
	for cut := 1; cut <= 3 && cut <= len(raw); cut++ {
		if b := raw[len(raw)-cut]; b < 0x80 {
			break
		} else if b >= 0xC0 {
			if !utf8.FullRuneInString(raw[len(raw)-cut:]) {
				raw = raw[:len(raw)-cut]
			}
			break
		}
	}
	var out string
	if json.Unmarshal([]byte(`"`+raw+`"`), &out) != nil {
		return ""
	}
	return out
}

// onlyKnown drops cited ids the evidence does not hold, so a draft never shows a
// footnote that cannot be opened.
func onlyKnown(r Recap, in Input) Recap {
	ok := map[string]bool{}
	for _, e := range in.Evidence {
		ok[e.ID] = true
	}
	fix := func(c Claim) Claim {
		out := Claim{Sentences: []Sentence{}}
		for _, s := range c.Sentences {
			src := []string{}
			for _, id := range s.Sources {
				if ok[id] {
					src = append(src, id)
				}
			}
			out.Sentences = append(out.Sentences, Sentence{Text: s.Text, Sources: src, Partial: s.Partial})
		}
		return out
	}
	out := Recap{Summary: fix(r.Summary), Changes: []Claim{}, Next: []Claim{}}
	for _, c := range r.Changes {
		if len(c.Sentences) > 0 {
			out.Changes = append(out.Changes, fix(c))
		}
	}
	for _, c := range r.Next {
		if len(c.Sentences) > 0 {
			out.Next = append(out.Next, fix(c))
		}
	}
	return out
}
