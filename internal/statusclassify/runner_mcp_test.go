package statusclassify

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagedMCPServersAreListedToBeTurnedOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed.toml")
	os.WriteFile(path, []byte("[mcp_servers.vm-chrome]\ncommand = \"x\"\n\n[mcp_servers.laptop-chrome]\ncommand = \"y\"\n"), 0o644)
	old := managedCodexConfig
	managedCodexConfig = path
	defer func() { managedCodexConfig = old }()
	got := managedMCPServers()
	if len(got) != 2 || got[0] != "vm-chrome" || got[1] != "laptop-chrome" {
		t.Fatalf("%v", got)
	}
}
