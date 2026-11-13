// Package reports keeps report files for workstream tabs under the Commander
// home, named by their content hash so the app can load them by URL.
package reports

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const maxSize = 20 << 20

var kinds = map[string]bool{".html": true, ".htm": true, ".md": true, ".txt": true, ".json": true, ".svg": true, ".png": true, ".jpg": true, ".jpeg": true, ".pdf": true}

var nameRE = regexp.MustCompile(`^[0-9a-f]{32}\.[a-z]{2,4}$`)

// Dir is where reports live.
func Dir(home string) string { return filepath.Join(home, "reports") }

// ValidName is a name Import could have made.
func ValidName(name string) bool {
	return nameRE.MatchString(name) && kinds[filepath.Ext(name)]
}

// Import copies a report file into the store and returns its name.
func Import(home, path string) (string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if !kinds[ext] {
		return "", fmt.Errorf("%s: a report is html, md, txt, json, svg, png, jpg or pdf", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > maxSize {
		return "", fmt.Errorf("%s: larger than 20 MB", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:16]) + ext
	if err := os.MkdirAll(Dir(home), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(Dir(home), name), data, 0o644); err != nil {
		return "", err
	}
	return name, nil
}
