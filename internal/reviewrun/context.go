package reviewrun

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ContextArtifact binds supplied historical context, not current action authority.
// Source identifies the owner's input; SHA256 identifies the exact copied bytes.
type ContextArtifact struct {
	Source string `json:"source"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func stageContext(dir string, paths []string) ([]ContextArtifact, error) {
	var artifacts []ContextArtifact
	if len(paths) == 0 {
		return artifacts, nil
	}
	if err := os.Mkdir(filepath.Join(dir, "context"), 0700); err != nil {
		return nil, err
	}
	for i, path := range paths {
		source, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(source)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > 8<<20 {
			return nil, fmt.Errorf("context must be a regular file at most 8 MiB: %s", source)
		}
		b, err := os.ReadFile(source)
		if err != nil {
			return nil, err
		}
		if len(b) > 8<<20 {
			return nil, fmt.Errorf("context exceeds 8 MiB")
		}
		relative := fmt.Sprintf("context/%04d.txt", i)
		if err := os.WriteFile(filepath.Join(dir, relative), b, 0400); err != nil {
			return nil, err
		}
		artifacts = append(artifacts, ContextArtifact{Source: source, Path: relative, SHA256: digest(b)})
	}
	return artifacts, nil
}

// privateGit redirects read-only merge inspection's transient objects away from
// the shared object store. It grants no sandbox access: the caller must already
// have permission to write the private run directory, otherwise Git fails closed.
func privateGit(ctx context.Context, req Request) ([]string, string, error) {
	if !req.PrivateGit {
		return nil, "", nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", req.Repo, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	b, err := cmd.Output()
	if err != nil {
		return nil, "", err
	}
	objects := strings.TrimSpace(string(b))
	// Alternates files cannot represent paths containing newlines.
	if !filepath.IsAbs(objects) || strings.ContainsAny(objects, "\r\n") {
		return nil, "", fmt.Errorf("unsupported Git object directory")
	}
	scratch := filepath.Join(req.Dir, "git-scratch")
	privateObjects := filepath.Join(scratch, "objects")
	if err := os.MkdirAll(filepath.Join(privateObjects, "info"), 0700); err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(filepath.Join(privateObjects, "info", "alternates"), []byte(objects+"\n"), 0400); err != nil {
		return nil, "", err
	}
	return []string{"GIT_OBJECT_DIRECTORY=" + privateObjects, "GIT_ALTERNATE_OBJECT_DIRECTORIES=", "GIT_TMPDIR=" + scratch, "TMPDIR=" + scratch, "GIT_OPTIONAL_LOCKS=0"}, scratch, nil
}
