package context

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDocumentFilesWalksMarkdownAndSkipsVendorTrees(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("readme.md", "# Root\n")
	write("docs/runtime.md", "# Runtime\n")
	write("docs/nested/protocol.markdown", "# Protocol\n")
	write("docs/notes.txt", "not markdown")
	write(".git/HEAD", "ref")
	write(".hidden/secret.md", "# Hidden\n")
	write("node_modules/pkg/README.md", "# Dep\n")
	write("vendor/lib/doc.md", "# Vendored\n")

	got, err := DocumentFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"docs/nested/protocol.markdown", "docs/runtime.md", "readme.md"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestDocumentFilesRejectsAFileAndAMissingPath(t *testing.T) {
	file := filepath.Join(t.TempDir(), "only.md")
	if err := os.WriteFile(file, []byte("# X\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := DocumentFiles(file); !IsCode(err, ErrInvalidQuery) {
		t.Fatalf("file = %v, want INVALID_QUERY", err)
	}
	if _, err := DocumentFiles(filepath.Join(t.TempDir(), "gone")); !IsCode(err, ErrSourceUnavailable) {
		t.Fatalf("missing = %v, want SOURCE_UNAVAILABLE", err)
	}
	if _, err := DocumentFiles(""); !IsCode(err, ErrInvalidQuery) {
		t.Fatalf("empty = %v, want INVALID_QUERY", err)
	}
}
