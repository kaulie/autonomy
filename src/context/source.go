package context

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SourceLoader reads a Resource's bytes from its Source. It is the seam that
// keeps "Source of Truth != Index" honest: the index can always be rebuilt by
// loading the source again.
type SourceLoader interface {
	// Load returns the source content and, for a repository source, its
	// revision (git commit SHA). A read failure is a SOURCE_UNAVAILABLE error.
	Load(ctx context.Context, r Resource) ([]byte, string, error)
}

// FileSourceLoader reads local files: Source.Location (a file or a directory)
// plus optional Source.Path. It computes a git revision for git sources when
// the checkout is local, and leaves Revision empty otherwise.
type FileSourceLoader struct{}

func (FileSourceLoader) Load(ctx context.Context, r Resource) ([]byte, string, error) {
	path := sourcePath(r)
	if path == "" {
		return nil, "", Errorf(ErrSourceUnavailable, "resource %q (%s) has no readable source location", r.ID, r.Name)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, "", Wrap(ErrSourceUnavailable, err, "read source for %q (%s)", r.ID, r.Name)
	}
	return content, gitRevision(ctx, r, path), nil
}

func sourcePath(r Resource) string {
	location := strings.TrimSpace(r.Source.Location)
	path := strings.TrimSpace(r.Source.Path)
	switch {
	case location == "":
		return path
	case path == "":
		return location
	}
	if info, err := os.Stat(location); err == nil && info.IsDir() {
		return filepath.Join(location, path)
	}
	return location
}

// gitRevision best-effort resolves the commit SHA of a git-sourced resource.
// A source that is not git, or has no local checkout, simply has no revision.
func gitRevision(ctx context.Context, r Resource, path string) string {
	if !strings.EqualFold(strings.TrimSpace(r.Source.Type), "git") {
		return r.Revision
	}
	dir := strings.TrimSpace(r.Source.Location)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		dir = filepath.Dir(path)
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return r.Revision
	}
	return strings.TrimSpace(string(out))
}
