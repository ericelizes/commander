package handoff

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// ErrNoArtifact is returned by ParseOutputFile when the output.json file
// does not exist. Callers treat this as "Task is not a producer" and let
// the Task close without any derived spawn — it is not an error condition.
var ErrNoArtifact = errors.New("handoff: output.json not present")

// ParseOutputFile reads the file at path, json.Unmarshals it into an
// Output, and runs ValidateOutput. Returns ErrNoArtifact (sentinel) when
// the file is absent so callers can distinguish "no producer artifact" from
// "malformed artifact".
func ParseOutputFile(path string) (*Output, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoArtifact
	}
	if err != nil {
		return nil, fmt.Errorf("handoff: read %s: %w", path, err)
	}
	var o Output
	if err := json.Unmarshal(data, &o); err != nil {
		return nil, fmt.Errorf("handoff: parse %s: %w", path, err)
	}
	if err := ValidateOutput(&o); err != nil {
		return nil, err
	}
	return &o, nil
}

// ValidateOutput rejects:
//   - missing or unknown schema (names observed + supported in the error)
//   - empty entries slice (producers must declare at least one derived seed)
//   - any entry with an empty title (would yield a nameless derived bead)
//
// Description and Context are not validated — they are free-form.
func ValidateOutput(o *Output) error {
	if o == nil {
		return errors.New("handoff: nil output")
	}
	if o.Schema == "" {
		return fmt.Errorf("handoff: missing schema field (supported: %s)", SchemaV1)
	}
	if o.Schema != SchemaV1 {
		return fmt.Errorf("handoff: unsupported output schema %q (supported: %s)", o.Schema, SchemaV1)
	}
	if len(o.Entries) == 0 {
		return errors.New("handoff: entries must be non-empty")
	}
	for i, e := range o.Entries {
		if e.Title == "" {
			return fmt.Errorf("handoff: entries[%d]: title is required", i)
		}
	}
	return nil
}
