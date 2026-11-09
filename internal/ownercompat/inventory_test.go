package ownercompat

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestTermInventoryHonorsScopedAllowlist(t *testing.T) {
	for _, bin := range []string{"git", "jq", "bash"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " unavailable")
		}
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "scripts"), 0755); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("../../scripts/term-inventory.sh")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"scripts/term-inventory.sh": script, "scripts/forbidden-terms.txt": []byte("Acme\nmodule.example/path\n"), "scripts/term-allowlist.txt": []byte("allowed.txt:Acme # reader\n*:module.example/path # stable module\n"), "allowed.txt": []byte("Acme\n"), "remaining.txt": []byte("Acme\nmodule.example/path\n")}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	c := exec.Command("bash", "scripts/term-inventory.sh", "--json")
	c.Dir = dir
	raw, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	var report struct {
		Total int
		Files int
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Total != 1 || report.Files != 1 {
		t.Fatalf("%s", raw)
	}
}
