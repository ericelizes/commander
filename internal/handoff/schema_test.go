package handoff

import "testing"

func TestSchemaV1Const(t *testing.T) {
	if SchemaV1 != "task-output@v1" {
		t.Fatalf("SchemaV1 = %q; want %q", SchemaV1, "task-output@v1")
	}
}
