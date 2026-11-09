package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// MaxDecisionOptions is how many options a card can offer (keys 1-9).
const MaxDecisionOptions = 9

// DecisionOption is one choice on a decision card.
type DecisionOption struct {
	Label        string `json:"label"`
	Implications string `json:"implications,omitempty"`
	Recommended  bool   `json:"recommended,omitempty"`
	// Approves marks the option that approves a gate: picking it closes the gate bead.
	Approves bool `json:"approves,omitempty"`
}

// Decision is the card a Manager writes when it flags a step for the user: what he
// is deciding, the context, the options (each with what it implies), an optional
// free-text field and, for "do this" asks, the exact commands to copy.
type Decision struct {
	Question string           `json:"question"`
	Context  string           `json:"context,omitempty"`
	Options  []DecisionOption `json:"options,omitempty"`
	Input    bool             `json:"input,omitempty"`
	Steps    []string         `json:"steps,omitempty"`
}

// ParseDecision decodes and validates a decision card.
func ParseDecision(raw string) (*Decision, error) {
	var d Decision
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("decision is not valid JSON {question, context, options[{label, implications, recommended, approves}], input, steps[]}: %w", err)
	}
	d.Question = strings.TrimSpace(d.Question)
	if d.Question == "" {
		return nil, errors.New("decision needs a one-line \"question\": what the user is deciding or doing")
	}
	if len(d.Options) > MaxDecisionOptions {
		return nil, fmt.Errorf("decision has %d options; at most %d", len(d.Options), MaxDecisionOptions)
	}
	if len(d.Options) == 0 && len(d.Steps) == 0 && !d.Input {
		return nil, errors.New("decision needs something to answer with: options[], steps[] or input")
	}
	for i := range d.Options {
		d.Options[i].Label = strings.TrimSpace(d.Options[i].Label)
		if d.Options[i].Label == "" {
			return nil, fmt.Errorf("option %d has no label", i+1)
		}
	}
	return &d, nil
}

// JSON is the stored form of the card.
func (d *Decision) JSON() string {
	b, _ := json.Marshal(d)
	return string(b)
}
