package context

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DocumentFiles lists Markdown files under root, recursively. Hidden
// directories (".git", …) and common vendor trees are skipped. Paths are
// relative to root, slash-separated. root must exist and be a directory.
func DocumentFiles(root string) ([]string, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || root == "." {
		return nil, Errorf(ErrInvalidQuery, "directory location is required")
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, Wrap(ErrSourceUnavailable, err, "read directory %q", root)
	}
	if !info.IsDir() {
		return nil, Errorf(ErrInvalidQuery, "location %q is a file; use shape file", root)
	}
	var out []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := d.Name()
		if d.IsDir() {
			if skipWalkDir(name) {
				return fs.SkipDir
			}
			return nil
		}
		if !isMarkdownName(name) {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, Wrap(ErrSourceUnavailable, err, "walk directory %q", root)
	}
	sort.Strings(out)
	return out, nil
}

func skipWalkDir(name string) bool {
	if name == "." || name == ".." {
		return false
	}
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "node_modules", "vendor", "third_party":
		return true
	default:
		return false
	}
}

func isMarkdownName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown":
		return true
	default:
		return false
	}
}
