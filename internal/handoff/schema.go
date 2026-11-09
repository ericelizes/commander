// Package handoff implements the producer-Task output artifact convention
// (U16). When a Task is a "producer" (e.g. "find 20 libraries to evaluate"), the
// operator writes a versioned JSON artifact to
// `<worktree>/.commander/output.json` (code-type) or
// `<projectRoot>/.commander/output.json` (knowledge-type) before closing the
// Task. When the producer Task closes, the Lead parses this artifact and
// spawns one derived Task per entry.
//
// The schema is versioned so future shape changes can be detected and
// rejected loudly rather than misparsed silently.
package handoff

// SchemaV1 is the only currently-supported `schema` value. Producers must
// emit this exact string in their output.json. Consumers reject any other
// value (including empty) with a clear error naming the version observed
// and the version supported.
const SchemaV1 = "task-output@v1"

// Output is the top-level shape of a producer's output.json. New fields may
// be added in a backwards-compatible way; breaking shape changes must bump
// the schema version.
type Output struct {
	// Schema must equal SchemaV1. Required.
	Schema string `json:"schema"`
	// Entries is the list of derived-Task seeds. Each entry produces one
	// derived Task bead. Order is preserved into the spawn-or-queue
	// decision (entries[0] spawns first, so producers should put the
	// highest-value items first).
	Entries []OutputEntry `json:"entries"`
}

// OutputEntry is one row in Output.Entries. Title is required; everything
// else is optional. Context is free-form per-producer — the Lead does not
// interpret it, only forwards it into the derived operator's bootstrap
// prompt.
type OutputEntry struct {
	// Title becomes the derived Task bead's title (visible in `bd list`).
	// Must be non-empty.
	Title string `json:"title"`
	// Description is appended to the derived operator's bootstrap prompt
	// as upstream context. Optional but strongly encouraged — without it,
	// the derived operator has only a title to work from.
	Description string `json:"description"`
	// Context is free-form structured data passed through to the derived
	// operator's bootstrap prompt. Producers use this for fields like
	// library, url, deadline, score, etc. Optional.
	Context map[string]interface{} `json:"context,omitempty"`
}
