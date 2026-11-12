// Package workflowtemplate loads, validates and instantiates a reusable
// workflow template (workflows/<id>.json): a fixed chain of steps with stable
// ids that cmdr workflow plug turns into ordinary beads. No engine: the
// template only says which steps exist and in what order.
package workflowtemplate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"github.com/example-user/commander/workflows"
)

// SchemaID is the only schema a template file may declare.
const SchemaID = "workflow-template@v1"

// Label prefixes the generated beads carry. All are reserved: only
// cmdr workflow plug writes them.
const (
	LabelTemplate = "workflow-template:"
	LabelVersion  = "workflow-version:"
	LabelInstance = "workflow-instance:"
	LabelStep     = "workflow-step:"
	LabelHash     = "workflow-hash:"
	LabelPrefix   = "workflow-"
)

var (
	Kinds  = []string{"task", "checkpoint"}
	Actors = []string{"operator", "separate-reviewer", "automatic", "owner"}
	// Verifiers are the supported check ids (never arbitrary code). Their
	// checks run in later steps; here a step only has to name a known one.
	// review-clean, recheck-clean and head-current only judge blocks plugged
	// from the retired six-step pr-review (version 1) snapshot.
	Verifiers = []string{"devin-comments-resolved", "review-report", "review-clean", "recheck-clean", "head-current", "threads-resolved", "ci-green", "merged", "deploy-ok", "review-proof"}
	// CatalogVerifiers name evidence no close door checks yet: the agent's
	// recorded comment, or the owner's recorded answer. Only a catalog-only
	// template may use them.
	CatalogVerifiers = []string{"recorded", "owner-answer"}
)

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// Step is one step of a template. After lists the step ids it waits on; empty
// means the step before it in the file (none for the first).
type Step struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Kind     string   `json:"kind"`
	Verifier string   `json:"verifier"`
	Actor    string   `json:"actor"`
	After    []string `json:"after,omitempty"`
	// Optional marks a step a planner may skip with a reason. Catalog-only
	// templates alone may mark one: the merge rule requires every step.
	Optional bool `json:"optional,omitempty"`
}

// Template is a parsed, validated template file. AppliesTo, Summary, UseWhen
// and SkipWhen are the catalog's plain words for when to use it; planners read
// them before plugging. A CatalogOnly template is shown and read, never plugged.
type Template struct {
	Schema      string   `json:"schema"`
	ID          string   `json:"id"`
	Version     int      `json:"version"`
	Title       string   `json:"title"`
	AppliesTo   string   `json:"applies_to,omitempty"`
	Summary     string   `json:"summary,omitempty"`
	UseWhen     []string `json:"use_when,omitempty"`
	SkipWhen    []string `json:"skip_when,omitempty"`
	CatalogOnly bool     `json:"catalog_only,omitempty"`
	Steps       []Step   `json:"steps"`
}

// Parse decodes and validates a template; unknown fields, a missing field, an
// unknown verifier/kind/actor, a duplicate id or a cycle fail.
func Parse(data []byte) (*Template, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var t Template
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("workflow template: %w", err)
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return &t, nil
}

// Load reads the embedded workflows/<id>.json.
func Load(id string) (*Template, error) {
	if !idRe.MatchString(id) {
		return nil, fmt.Errorf("workflow template %q: not a template id", id)
	}
	data, err := fs.ReadFile(workflows.FS, id+".json")
	if err != nil {
		return nil, fmt.Errorf("workflow template %q not found (known: %s)", id, strings.Join(Names(), ", "))
	}
	t, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if t.ID != id {
		return nil, fmt.Errorf("workflow template file %s.json declares id %q", id, t.ID)
	}
	if strings.TrimSpace(t.AppliesTo) == "" || strings.TrimSpace(t.Summary) == "" || len(t.UseWhen) == 0 || len(t.SkipWhen) == 0 {
		return nil, fmt.Errorf("workflow template %s: a catalog template needs applies_to, summary, use_when and skip_when", id)
	}
	return t, nil
}

// Catalog loads every embedded template, in id order.
func Catalog() ([]*Template, error) {
	var out []*Template
	for _, id := range Names() {
		t, err := Load(id)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// Names lists the embedded template ids.
func Names() []string {
	ents, _ := fs.ReadDir(workflows.FS, ".")
	var out []string
	for _, e := range ents {
		if n, ok := strings.CutSuffix(e.Name(), ".json"); ok {
			out = append(out, n)
		}
	}
	return out
}

func oneOf(v string, set []string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// Validate checks every required field and the step graph.
func (t *Template) Validate() error {
	if t.Schema != SchemaID {
		return fmt.Errorf("workflow template: schema must be %q, got %q", SchemaID, t.Schema)
	}
	if !idRe.MatchString(t.ID) {
		return fmt.Errorf("workflow template: id %q must be lowercase letters, digits and hyphens", t.ID)
	}
	if t.Version < 1 {
		return fmt.Errorf("workflow template %s: version must be 1 or more", t.ID)
	}
	if strings.TrimSpace(t.Title) == "" {
		return fmt.Errorf("workflow template %s: title is missing", t.ID)
	}
	if len(t.Steps) == 0 {
		return fmt.Errorf("workflow template %s: no steps", t.ID)
	}
	index := map[string]int{}
	for i, s := range t.Steps {
		switch {
		case !idRe.MatchString(s.ID):
			return fmt.Errorf("workflow template %s: step %d: id %q must be lowercase letters, digits and hyphens", t.ID, i+1, s.ID)
		case strings.TrimSpace(s.Title) == "":
			return fmt.Errorf("workflow template %s: step %s: title is missing", t.ID, s.ID)
		case !oneOf(s.Kind, Kinds):
			return fmt.Errorf("workflow template %s: step %s: kind %q is not one of %s", t.ID, s.ID, s.Kind, strings.Join(Kinds, ", "))
		case !oneOf(s.Verifier, Verifiers) && !(t.CatalogOnly && oneOf(s.Verifier, CatalogVerifiers)):
			return fmt.Errorf("workflow template %s: step %s: unknown verifier %q (known: %s)", t.ID, s.ID, s.Verifier, strings.Join(Verifiers, ", "))
		case s.Optional && !t.CatalogOnly:
			return fmt.Errorf("workflow template %s: step %s: only a catalog-only template may mark a step optional", t.ID, s.ID)
		case !oneOf(s.Actor, Actors):
			return fmt.Errorf("workflow template %s: step %s: actor %q is not one of %s", t.ID, s.ID, s.Actor, strings.Join(Actors, ", "))
		}
		if _, dup := index[s.ID]; dup {
			return fmt.Errorf("workflow template %s: duplicate step id %q", t.ID, s.ID)
		}
		index[s.ID] = i
	}
	// Resolve waits, then look for a cycle (depth-first colouring).
	waits := t.Waits()
	for id, ws := range waits {
		for _, w := range ws {
			if _, ok := index[w]; !ok {
				return fmt.Errorf("workflow template %s: step %s waits on unknown step %q", t.ID, id, w)
			}
		}
	}
	state := map[string]int{}
	var visit func(id string) error
	visit = func(id string) error {
		switch state[id] {
		case 1:
			return fmt.Errorf("workflow template %s: cycle through step %q", t.ID, id)
		case 2:
			return nil
		}
		state[id] = 1
		for _, w := range waits[id] {
			if err := visit(w); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for _, s := range t.Steps {
		if err := visit(s.ID); err != nil {
			return err
		}
	}
	return nil
}

// Waits maps each step id to the step ids it waits on.
func (t *Template) Waits() map[string][]string {
	out := map[string][]string{}
	for i, s := range t.Steps {
		switch {
		case len(s.After) > 0:
			out[s.ID] = append([]string(nil), s.After...)
		case i > 0:
			out[s.ID] = []string{t.Steps[i-1].ID}
		default:
			out[s.ID] = nil
		}
	}
	return out
}

// Hash fingerprints the definition (12 hex of sha256 over its canonical JSON).
func (t *Template) Hash() string {
	b, _ := json.Marshal(t)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}
