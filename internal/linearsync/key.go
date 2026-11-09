package linearsync

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

var ErrKey = errors.New("Linear key unavailable; use cmdr linear set-key")

func KeyPath(home string) string { return filepath.Join(home, "linear.key") }

// ReadKey refuses symlinks and loose permissions. Errors never include contents.
func ReadKey(home string) (string, error) {
	path := KeyPath(home)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return "", ErrKey
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", ErrKey
	}
	key := strings.TrimSpace(string(raw))
	if strings.ContainsAny(key, "\r\n") || len(key) > 4096 {
		return "", ErrKey
	}
	return key, nil
}

// ValidateKey rejects empty, whitespace-containing, and oversized credentials.
func ValidateKey(key string) error {
	if key == "" || strings.IndexFunc(key, unicode.IsSpace) >= 0 || len(key) > 4096 {
		return ErrKey
	}
	return nil
}

// WriteKey atomically replaces the credential, including an existing symlink.
func WriteKey(home, key string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	if os.MkdirAll(home, 0700) != nil {
		return ErrKey
	}
	f, err := os.CreateTemp(home, ".linear-key-*")
	if err != nil {
		return ErrKey
	}
	defer os.Remove(f.Name())
	if f.Chmod(0600) != nil {
		f.Close()
		return ErrKey
	}
	if _, err = f.WriteString(key + "\n"); err != nil {
		f.Close()
		return ErrKey
	}
	if f.Close() != nil {
		return ErrKey
	}
	if os.Rename(f.Name(), KeyPath(home)) != nil {
		return ErrKey
	}
	return nil
}
