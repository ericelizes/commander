package store

import (
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
)

// Frozen from canonical main 21756bbedd15e953b21cd9366a5dfe6102ec8053.
func TestShippedMigrationBytesUnchanged(t *testing.T) {
	for path, want := range map[string]string{
		"bead_questions_schema.sql":                   "9670a19d6002911e6febee63265e4b0d53d30a29c48b6672511cae543cb1414a",
		"execution_schema.sql":                        "c4ab77339e951f226c54e1a67d4f4dfc01e47007fa48bfaca9b263eb9adf54c3",
		"jurisdiction_schema.sql":                     "f9846c376a9c1229b1c5656c595aca94c225a3ccdb4b3da39fcb993b189d21be",
		"migrations/0001_initial.sql":                 "1cdb3fd183bf7d5725544c0bc6e9f1faab71463bbec64f4f98e5aefba5f62543",
		"migrations/0002_am_agents.sql":               "eb4acbfcc794eb6f3f6f97d7beaf39c7e04fb14ebc4cc23e0cb71d1c2195c592",
		"migrations/0003_workstream_ranks.sql":        "b9530e32a90005e52f8a393f1bc1e2d2cf7d489a5599f1f0ead0557db6714ee9",
		"migrations/0004_workstream_managers.sql":     "c5483e4239895b78ca624258a8fd07d214811a91d770a5bf518183a823299663",
		"migrations/0005_inspector_tabs.sql":          "8724cd9e49891440e826f400e87d29745ea0e3f8aaa650b058cdc22bf928d87e",
		"migrations/0006_needs_you.sql":               "f133cded3b86a4be1f18ae2f3842c723882eb7e4a22c8ed493ba7fe98fd6a632",
		"migrations/0007_agent_state.sql":             "f35acd85fcceab21dc926319295545662223f4a5669b41b844e240ee932c6c17",
		"migrations/0008_needs_you_decision.sql":      "4a61573b56b8337ac834cea471fced4cfc9bcd297d0e8bc120e3acf59d82f8a1",
		"migrations/0009_gates.sql":                   "d2797c97132bd6710e0e9a386f67263dbf4e5e6d731b9defcf837419cdcdfa04",
		"migrations/0010_watch.sql":                   "44ed508a8841e286267761d7b8a721d9c4b8207bcf42975d8c7167b23cc36dae",
		"migrations/0011_chat_costs.sql":              "92bda438ddcb176fe0e1efaf5c30fb7c1191db9b6d79f57eb5028bbe00dc6c5d",
		"migrations/0012_project_repo_path.sql":       "612a5cbf4f97748899cbc85cf15930b2ecc94989a517ad6dc1c72e9ebd41aa59",
		"migrations/0013_usage_turns.sql":             "15b69f82ac11a9f6c992f7d8252cb696167daa8c1a283103c60f3ad0ae756e63",
		"migrations/0014_usage_rescan_chats.sql":      "5f9fb396fd5c0a27c9086b7202ee1196da11a68a6b0fd63877f9184e67e70181",
		"migrations/0015_pitches.sql":                 "f675ae0e72b235b6f4c8f2a6e50ea163f9f02dbd8d3af6ab1f9eef6f30b1c627",
		"migrations/0016_needs_resolutions.sql":       "20819ea975800aa6e5a896479441faa58be8b278dfed3a3627de34fa3bf8c8e5",
		"migrations/0017_resolution_generations.sql":  "1d30ed7a348b0ebc9648ce3b2b18f964c7161974d5a0d53f718e814d9e638f60",
		"migrations/0018_board_since.sql":             "8d1acd0d8be7f5155674ede8af29bd9326a57cce4aa257353b8554b16e63c308",
		"migrations/0019_pitch_basis.sql":             "b0845faf700b60227c8ef6a54f030a4d5de0eb50da5bca94a89f13134c6fc5d4",
		"migrations/0020_run_worktree.sql":            "d01effd5525cc459cfffcb12ad8437c55059978e24174d4a0bc042f71a6c959e",
		"migrations/0021_manager_unregistrations.sql": "6d8d7ed65287095193de8e96ed12eec1d4f2b3ca043f567cb1ba08e07bd56695",
		"migrations/0022_bead_ranks.sql":              "fc1ac42db4aeb2f3a7b9927a3c08b6e56a5019d6868d44e39c9130332240f6f8",
		"postgres_schema.sql":                         "4001d3136dfaf76424765fed8b935d5a6938aa26c514238d82d29505ba80092e",
		"runtime_integrity_schema.sql":                "bce51598e4a463934fdda0e65cf08ae9399129df34eb84f76b8301570e79be3a",
		"supervision_schema.sql":                      "94edb8041da119a4b132d8b5479444ae910edef36e4c5f86165c4895b48d7a0d",
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != want {
			t.Errorf("shipped migration %s changed: %s", path, got)
		}
	}
}
