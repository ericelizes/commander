package overview

import (
	"context"
	"encoding/json"
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
// Only sentences that are complete objects count; a sentence still being
// written is left out until its closing brace arrives.
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
			sent := Sentence{Text: s.Text, Sources: *s.Sources}
			sec := stack[0].key
			if sec == "summary" {
				r.Summary.Sentences = append(r.Summary.Sentences, sent)
				continue
			}
			list := &r.Changes
			if sec == "next" {
				list = &r.Next
			} else if sec != "changes" {
				continue
			}
			for len(*list) < claims[sec] {
				*list = append(*list, Claim{})
			}
			if n := claims[sec]; n > 0 {
				(*list)[n-1].Sentences = append((*list)[n-1].Sentences, sent)
			}
		}
	}
	return r
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
			out.Sentences = append(out.Sentences, Sentence{Text: s.Text, Sources: src})
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
