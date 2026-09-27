package githubauth

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const dirName = ".autonomy"

// WriteAccountToken writes a short-lived token under the account workspace
// root so every agent in that root can find it by walking up from cwd.
// Files are 0600; the directory is 0700.
func WriteAccountToken(root, token string) error {
	root = strings.TrimSpace(root)
	token = strings.TrimSpace(token)
	if root == "" || token == "" {
		return fmt.Errorf("githubauth: root and token are required")
	}
	dir := filepath.Join(root, dirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	tokenPath := filepath.Join(dir, tokenFileName)
	if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tokenPath, 0o600); err != nil {
		return err
	}
	// git credential store: https://x-access-token:TOKEN@github.com
	line := "https://x-access-token:" + token + "@github.com\n"
	credPath := filepath.Join(dir, credentialsFileName)
	if err := os.WriteFile(credPath, []byte(line), 0o600); err != nil {
		return err
	}
	return os.Chmod(credPath, 0o600)
}

// FindTokenFile walks from start up to the filesystem root looking for
// .autonomy/github-token. Used by host wrappers and tests.
func FindTokenFile(start string) string {
	dir := start
	for i := 0; i < 32; i++ {
		candidate := filepath.Join(dir, dirName, tokenFileName)
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}
