package beads

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// bd uses UUID strings; br and older exports use integer comment IDs. Keep
// both exact identities available without breaking existing export readers.
func (c *Comment) UnmarshalJSON(data []byte) error {
	var wire struct {
		ID        json.RawMessage `json:"id"`
		Author    string          `json:"author"`
		Text      string          `json:"text"`
		CreatedAt string          `json:"created_at"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	id := ""
	if len(wire.ID) > 0 && !bytes.Equal(wire.ID, []byte("null")) {
		if err := json.Unmarshal(wire.ID, &id); err != nil {
			n, e := strconv.ParseUint(string(wire.ID), 10, 64)
			if e != nil {
				return fmt.Errorf("invalid comment id: %w", e)
			}
			id = strconv.FormatUint(n, 10)
		}
	}
	*c = Comment{ID: id, Author: wire.Author, Text: wire.Text, CreatedAt: wire.CreatedAt}
	return nil
}
