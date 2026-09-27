package githubauth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAccountTokenPermissionsAndWalk(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "agent-10046", "autonomy")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteAccountToken(root, "ghs_test"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".autonomy")
	st, err := os.Stat(dir)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode=%v err=%v", st.Mode(), err)
	}
	tokenPath := filepath.Join(dir, "github-token")
	st, err = os.Stat(tokenPath)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("token mode=%v err=%v", st.Mode(), err)
	}
	raw, err := os.ReadFile(tokenPath)
	if err != nil || string(raw) != "ghs_test\n" {
		t.Fatalf("token contents %q", raw)
	}
	if got := FindTokenFile(nested); got != tokenPath {
		t.Fatalf("walk got %q want %q", got, tokenPath)
	}
}
