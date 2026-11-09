package reviewrun

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Recorded is original launch provenance and its independently stored terminal fact.
type Recorded struct {
	Manifest Manifest
	Terminal Result
}

func registryDir(root string, id int64) string { return filepath.Join(root, strconv.FormatInt(id, 10)) }

func registerManifest(root string, m Manifest, hash string) error {
	dir := registryDir(root, m.RunID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	actual, err := seal(filepath.Join(dir, hash+".json"), m)
	if err != nil {
		return err
	}
	if actual != hash {
		return fmt.Errorf("registered manifest digest mismatch")
	}
	return nil
}

// ReadRecorded never follows a caller-supplied artifact path from a bead comment.
func ReadRecorded(root string, id int64, hash string) (Recorded, error) {
	var r Recorded
	if _, err := hex.DecodeString(hash); err != nil || len(hash) != 64 || id <= 0 {
		return r, fmt.Errorf("invalid original review manifest identity")
	}
	dir := registryDir(root, id)
	b, err := os.ReadFile(filepath.Join(dir, hash+".json"))
	if err != nil {
		return r, err
	}
	if digest(b) != hash {
		return r, fmt.Errorf("original review manifest changed")
	}
	if err = json.Unmarshal(b, &r.Manifest); err != nil {
		return r, err
	}
	b, err = os.ReadFile(filepath.Join(dir, hash+".terminal.json"))
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(b, &r.Terminal); err != nil {
		return r, err
	}
	if r.Manifest.RunID != id || r.Terminal.ManifestHash != hash || r.Terminal.PID <= 0 || r.Terminal.Finished.Before(r.Terminal.Started) {
		return r, fmt.Errorf("original review terminal identity mismatch")
	}
	return r, nil
}
