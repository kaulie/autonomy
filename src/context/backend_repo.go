package context

import (
	"context"
	"os/exec"
	"strings"
)

// gitLocal is source_code_repository / git-local: a git checkout on the
// runtime host. Remote hosts (github, gitlab, …) are other providers.
type gitLocal struct{}

func (gitLocal) Kind() string       { return KindSourceCodeRepository }
func (gitLocal) Provider() string   { return ProviderGitLocal }
func (gitLocal) SourceType() string { return "git" }

func (gitLocal) List(ctx context.Context, location string, suffixes []string) ([]ListedDocument, error) {
	root := strings.TrimSpace(location)
	if looksRemoteLocation(root) {
		return nil, Errorf(ErrInvalidQuery, "source_code_repository/git-local needs a checkout path; remote hosts are other providers")
	}
	if err := requireGitCheckout(ctx, root); err != nil {
		return nil, err
	}
	files, err := DocumentFiles(root, suffixes)
	if err != nil {
		return nil, err
	}
	return listedFromFiles(root, files), nil
}

func requireGitCheckout(ctx context.Context, root string) error {
	if strings.TrimSpace(root) == "" {
		return Errorf(ErrInvalidQuery, "source_code_repository/git-local location is required")
	}
	cmd := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return Errorf(ErrInvalidQuery, "location %q is not a git checkout; use filesystem/local or a host provider", root)
	}
	return nil
}
