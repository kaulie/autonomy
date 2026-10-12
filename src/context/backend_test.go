package context

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestResolveBackendDefaultsAndRefusesUnimplemented(t *testing.T) {
	b, err := ResolveBackend("", "", "")
	if err != nil || b.Kind() != KindFilesystem || b.Provider() != ProviderLocal {
		t.Fatalf("empty = %v %v", b, err)
	}
	b, err = ResolveBackend("", "", "git")
	if err != nil || b.Kind() != KindSourceCodeRepository || b.Provider() != ProviderGitLocal {
		t.Fatalf("legacy git = %v %v", b, err)
	}
	if _, err := ResolveBackend(KindCloud, "", ""); !IsCode(err, ErrInvalidQuery) {
		t.Fatalf("cloud without provider = %v", err)
	}
	if _, err := ResolveBackend(KindCloud, ProviderS3, ""); !IsCode(err, ErrInvalidQuery) {
		t.Fatalf("cloud/s3 = %v", err)
	}
	if _, err := ResolveBackend(KindSourceCodeRepository, ProviderGitHub, ""); !IsCode(err, ErrInvalidQuery) {
		t.Fatalf("github = %v", err)
	}
	if _, err := ResolveBackend(KindFilesystem, ProviderSFTP, ""); !IsCode(err, ErrInvalidQuery) {
		t.Fatalf("sftp = %v", err)
	}
	if _, err := ResolveBackend("wiki", "", ""); !IsCode(err, ErrInvalidQuery) {
		t.Fatalf("unknown kind = %v", err)
	}
}

func TestFilesystemLocalListsDocumentsAndRejectsURLs(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "readme.md"), []byte("# X\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := filesystemLocal{}.List(context.Background(), root, nil)
	if err != nil || len(got) != 1 || got[0].Name != "readme.md" {
		t.Fatalf("list = %+v, %v", got, err)
	}
	if _, err := (filesystemLocal{}).List(context.Background(), "https://example.com/docs", nil); !IsCode(err, ErrInvalidQuery) {
		t.Fatalf("url = %v", err)
	}
}

func TestGitLocalRequiresACheckout(t *testing.T) {
	plain := t.TempDir()
	if err := os.WriteFile(filepath.Join(plain, "readme.md"), []byte("# X\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (gitLocal{}).List(context.Background(), plain, nil); !IsCode(err, ErrInvalidQuery) {
		t.Fatalf("plain dir = %v, want INVALID_QUERY", err)
	}
	if _, err := (gitLocal{}).List(context.Background(), "git@host:org/repo.git", nil); !IsCode(err, ErrInvalidQuery) {
		t.Fatalf("remote = %v", err)
	}

	repo := t.TempDir()
	run := exec.Command("git", "-C", repo, "init")
	run.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "readme.md"), []byte("# X\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("skip"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := gitLocal{}.List(context.Background(), repo, nil)
	if err != nil || len(got) != 1 || got[0].Name != "readme.md" {
		t.Fatalf("git list = %+v, %v", got, err)
	}
	src := StampSource(ResourceSource{Location: repo}, gitLocal{})
	if src.Kind != KindSourceCodeRepository || src.Provider != ProviderGitLocal || src.Type != "git" {
		t.Fatalf("stamp = %+v", src)
	}
}
