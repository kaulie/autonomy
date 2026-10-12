package context

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultDocumentSuffixes is the set a directory walk uses when the
// operator does not filter: every Markdown document.
var DefaultDocumentSuffixes = []string{".md", ".markdown"}

// DocumentFiles lists documents under root, recursively. suffixes filters
// by extension (".md", "md", "*.markdown"); empty means
// DefaultDocumentSuffixes — search all available documents. Hidden
// directories (".git", …) and common vendor trees are skipped. Paths are
// relative to root, slash-separated. root must exist and be a directory.
func DocumentFiles(root string, suffixes []string) ([]string, error) {
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
	want, err := documentSuffixSet(suffixes)
	if err != nil {
		return nil, err
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
		if !want[strings.ToLower(filepath.Ext(name))] {
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

// documentSuffixSet normalizes operator suffixes. Empty input is all
// default document suffixes. Tokens may carry a leading dot or "*.".
func documentSuffixSet(suffixes []string) (map[string]bool, error) {
	out := make(map[string]bool)
	saw := false
	for _, raw := range suffixes {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		saw = true
		s := normalizeDocumentSuffix(raw)
		if s == "" {
			return nil, Errorf(ErrInvalidQuery, "suffix %q is not a file extension", raw)
		}
		out[s] = true
	}
	if !saw {
		for _, s := range DefaultDocumentSuffixes {
			out[s] = true
		}
	}
	return out, nil
}

func normalizeDocumentSuffix(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimPrefix(s, "*")
	if s == "" || s == "." {
		return ""
	}
	if !strings.HasPrefix(s, ".") {
		s = "." + s
	}
	if s == "." || strings.ContainsAny(s, `/\:`) {
		return ""
	}
	return s
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
