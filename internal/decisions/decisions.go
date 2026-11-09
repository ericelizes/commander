// Package decisions keeps the user's standing decisions: calls that hold until
// the user changes them (invented examples: "catalog changes require CI",
// "no deploy windows unless asked"). They live in $COMMANDER_HOME/decisions/decisions.json;
// the mail hook hands each Commander and Manager turn the ones in its scope
// (Deliver), and the write door refuses a write that names what a decision's
// rule rules out (Contradicts).
package decisions

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// Global is the scope of a decision that holds in every workstream.
const Global = "global"

// MaxWords caps the user's words on one decision: a decision is one call.
const MaxWords = 300

// Decision is one standing call, in the user's words.
type Decision struct {
	ID    string `json:"id"`
	Words string `json:"words"`
	// Rule is a key no-<slug>: an agent write whose gate, wait, park, step or
	// ask text names <slug> is refused with the decision quoted.
	Rule    string `json:"rule,omitempty"`
	Scope   string `json:"scope"` // Global or a workstream (or track) bead id
	Date    string `json:"date"`  // YYYY-MM-DD the user said it
	AddedAt int64  `json:"added_at"`
	AddedBy string `json:"added_by"`

	RetiredAt    int64  `json:"retired_at,omitempty"`
	RetiredBy    string `json:"retired_by,omitempty"`
	RetireReason string `json:"retire_reason,omitempty"`
}

// Active reports whether the decision still holds.
func (d Decision) Active() bool { return d.RetiredAt == 0 }

// Line is the decision as an agent reads it.
func (d Decision) Line() string {
	meta := d.Scope + ", " + d.Date
	if d.Rule != "" {
		meta += ", rule " + d.Rule
	}
	return fmt.Sprintf("%s (%s): %q", d.ID, meta, d.Words)
}

type file struct {
	Next      int        `json:"next"`
	Decisions []Decision `json:"decisions"`
}

// Dir is where the decisions and their delivery records live.
func Dir(home string) string { return filepath.Join(home, "decisions") }

func storePath(home string) string { return filepath.Join(Dir(home), "decisions.json") }

// Load is every decision, retired ones included, oldest first. A missing file is none.
func Load(home string) ([]Decision, error) {
	f, err := read(home)
	return f.Decisions, err
}

// ActiveIn is the decisions that hold for scopes: the global ones and those
// scoped to any of scopes. No scopes = every active decision (Commander's view).
func ActiveIn(ds []Decision, scopes ...string) []Decision {
	var out []Decision
	for _, d := range ds {
		if !d.Active() {
			continue
		}
		if len(scopes) == 0 || d.Scope == Global {
			out = append(out, d)
			continue
		}
		for _, s := range scopes {
			if d.Scope == s {
				out = append(out, d)
				break
			}
		}
	}
	return out
}

func read(home string) (file, error) {
	var f file
	raw, err := os.ReadFile(storePath(home))
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, fmt.Errorf("decisions file %s is unreadable: %w", storePath(home), err)
	}
	return f, nil
}

var ruleKey = regexp.MustCompile(`^no-[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidRule refuses a rule key the door cannot match.
func ValidRule(rule string) error {
	if rule == "" || ruleKey.MatchString(rule) {
		return nil
	}
	return fmt.Errorf("--rule %q: a rule key is no-<words joined by dashes> (e.g. no-deploy-window); a write whose text names those words is refused", rule)
}

var date = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// Add records d (Words and Scope required; Date defaults to now) and returns it with its id.
func Add(home string, d Decision, now time.Time) (Decision, error) {
	d.Words = strings.TrimSpace(d.Words)
	if d.Words == "" {
		return d, errors.New("--words is required: the user's own words for the call")
	}
	if utf8.RuneCountInString(d.Words) > MaxWords {
		return d, fmt.Errorf("--words is %d characters (max %d): a standing decision is one call, in the user's words", utf8.RuneCountInString(d.Words), MaxWords)
	}
	if err := ValidRule(d.Rule); err != nil {
		return d, err
	}
	if d.Scope == "" {
		d.Scope = Global
	}
	if d.Date == "" {
		d.Date = now.Format("2006-01-02")
	} else if !date.MatchString(d.Date) {
		return d, fmt.Errorf("--date %q: want YYYY-MM-DD", d.Date)
	}
	d.AddedAt = now.Unix()
	err := update(home, func(f *file) error {
		for _, o := range f.Decisions {
			if o.Active() && o.Scope == d.Scope && o.Words == d.Words {
				d = o
				return nil // the same call again: keep the one recorded
			}
		}
		f.Next++
		d.ID = fmt.Sprintf("d%d", f.Next)
		f.Decisions = append(f.Decisions, d)
		return nil
	})
	return d, err
}

// Retire marks decision id as no longer holding (the user changed the call).
func Retire(home, id, by, reason string, now time.Time) (Decision, error) {
	var out Decision
	err := update(home, func(f *file) error {
		for i := range f.Decisions {
			if f.Decisions[i].ID != id {
				continue
			}
			if !f.Decisions[i].Active() {
				return fmt.Errorf("%s is already retired", id)
			}
			f.Decisions[i].RetiredAt, f.Decisions[i].RetiredBy, f.Decisions[i].RetireReason = now.Unix(), by, strings.TrimSpace(reason)
			out = f.Decisions[i]
			return nil
		}
		return fmt.Errorf("no decision %s (cmdr decisions list)", id)
	})
	return out, err
}

// update applies change under an exclusive lock and writes the file atomically.
func update(home string, change func(*file) error) error {
	if err := os.MkdirAll(Dir(home), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(Dir(home), "decisions.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	f, err := read(home)
	if err != nil {
		return err
	}
	if err := change(&f); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(storePath(home), append(raw, '\n'))
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// slug is text lowercased with every run of other characters one dash.
func slug(s string) string {
	return "-" + strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-") + "-"
}

// Contradicts is the first decision among ds whose rule no-<x> the texts name
// (whole words: "the deploy window opens" names deploy-window), or nil. Only
// rule keys are matched; the words are never interpreted.
func Contradicts(ds []Decision, texts ...string) *Decision {
	for i, d := range ds {
		key, ok := strings.CutPrefix(d.Rule, "no-")
		if !d.Active() || !ok || key == "" {
			continue
		}
		for _, t := range texts {
			if strings.Contains(slug(t), "-"+key+"-") {
				return &ds[i]
			}
		}
	}
	return nil
}
