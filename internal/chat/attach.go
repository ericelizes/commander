package chat

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Attachment is a file sent with a user message. Data is the file's bytes (the
// API takes them base64 in JSON); Path is where the engine saved them.
type Attachment struct {
	Name string `json:"name"`
	Mime string `json:"mime,omitempty"`
	Data []byte `json:"data,omitempty"`
	Path string `json:"path,omitempty"`
}

const (
	maxAttachments = 10
	maxImageBytes  = 5 << 20 // the API's per-image limit
	maxFileBytes   = 25 << 20
)

var imageMimes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// IsImage reports whether the attachment goes to claude as an image block.
func (a Attachment) IsImage() bool { return imageMimes[a.Mime] }

// SendWithAttachments writes a user message with files. Each file is saved to
// <Home>/uploads/<role>/<ts>-<n>-<name>. Images go to claude as base64 image
// blocks; other files are named by absolute path in the message text.
func (e *Engine) SendWithAttachments(ctx context.Context, role, text string, files []Attachment) error {
	if len(files) == 0 {
		return e.Send(ctx, role, text)
	}
	if _, _, err := e.live(role); err != nil {
		return err
	}
	saved, err := e.SaveUploads(role, files)
	if err != nil {
		return err
	}
	return e.send(role, text, "user", saved...)
}

// SaveUploads writes files under <Home>/uploads/<role> and returns them with Path set.
func (e *Engine) SaveUploads(role string, files []Attachment) ([]Attachment, error) {
	// Page captures ride along unasked: when they would push the send over the
	// cap they are dropped, never the user's message.
	if len(files) > maxAttachments {
		kept := make([]Attachment, 0, len(files))
		for _, f := range files {
			if !strings.HasPrefix(f.Name, captureNamePrefix) {
				kept = append(kept, f)
			}
		}
		files = kept
	}
	if len(files) > maxAttachments {
		return nil, fmt.Errorf("chat: at most %d attachments", maxAttachments)
	}
	dir := filepath.Join(e.opts.Home, "uploads", role)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	stamp := time.Now().Format("20060102-150405")
	out := make([]Attachment, 0, len(files))
	for _, f := range files {
		if strings.HasPrefix(f.Name, captureNamePrefix) {
			SweepCaptures(e.opts.Home, captureMaxAge, time.Now())
			break
		}
	}
	for i, f := range files {
		limit := maxFileBytes
		if f.IsImage() {
			limit = maxImageBytes
		}
		if len(f.Data) == 0 {
			return nil, fmt.Errorf("chat: attachment %q is empty", f.Name)
		}
		if len(f.Data) > limit {
			return nil, fmt.Errorf("chat: attachment %q is larger than %d MB", f.Name, limit>>20)
		}
		f.Path = filepath.Join(dir, fmt.Sprintf("%s-%d-%s", stamp, i, safeName(f.Name)))
		if err := os.WriteFile(f.Path, f.Data, 0o600); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// safeName keeps a base name that is safe as one path element.
func safeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`/:*?"<>|`, r) {
			return '_'
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		return "file"
	}
	return name
}

// WithPaths appends the saved files' paths to text (for a chat that only takes text).
func WithPaths(text string, files []Attachment) string {
	if len(files) == 0 {
		return text
	}
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}
	return strings.TrimSpace(text + "\n\nAttached files:\n" + strings.Join(paths, "\n"))
}

// userContent is the message content: the plain text, or blocks when files ride along.
func userContent(text string, files []Attachment) any {
	if len(files) == 0 {
		return text
	}
	var others []Attachment
	var images []map[string]any
	for _, f := range files {
		if f.IsImage() {
			images = append(images, map[string]any{"type": "image", "source": map[string]any{
				"type": "base64", "media_type": f.Mime, "data": base64.StdEncoding.EncodeToString(f.Data)}})
		} else {
			others = append(others, f)
		}
	}
	text = WithPaths(text, others)
	blocks := []map[string]any{}
	if strings.TrimSpace(text) != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
	}
	return append(blocks, images...)
}

// The app attaches what the user's focused browser tab shows as capture-*.png
// and capture-*.txt. They hold page contents, so they do not stay: every upload
// of one deletes the captures older than captureMaxAge.
const (
	captureNamePrefix = "capture-"
	captureMaxAge     = 24 * time.Hour
)

// capturedFile is exactly the shape SaveUploads gives the app's capture files
// (<stamp>-<n>-capture-<digits>.png|txt); a user file that merely contains
// "capture-" never matches.
var capturedFile = regexp.MustCompile(`^\d{8}-\d{6}-\d+-capture-\d{14}\.(png|txt)$`)

// SweepCaptures removes saved captures (<Home>/uploads/<role>/<stamp>-<n>-capture-*)
// last written more than maxAge before now. It returns how many it removed.
func SweepCaptures(home string, maxAge time.Duration, now time.Time) int {
	matches, _ := filepath.Glob(filepath.Join(home, "uploads", "*", "*-"+captureNamePrefix+"*"))
	n := 0
	for _, m := range matches {
		if !capturedFile.MatchString(filepath.Base(m)) {
			continue
		}
		fi, err := os.Lstat(m)
		if err != nil || !fi.Mode().IsRegular() || now.Sub(fi.ModTime()) <= maxAge {
			continue
		}
		if os.Remove(m) == nil {
			n++
		}
	}
	return n
}

// RunCaptureSweep deletes old captures now and then every interval until ctx
// ends, so they go even when the user stops using capture.
func RunCaptureSweep(ctx context.Context, home string, interval time.Duration) {
	SweepCaptures(home, captureMaxAge, time.Now())
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			SweepCaptures(home, captureMaxAge, now)
		}
	}
}
