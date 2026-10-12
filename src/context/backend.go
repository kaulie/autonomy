package context

import (
	"context"
	"strings"
)

// Knowledge-base kinds. Each kind has its own management model; each
// provider is a separate implementation of that model.
const (
	KindFilesystem           = "filesystem"
	KindSourceCodeRepository = "source_code_repository"
	KindCloud                = "cloud"
)

// Implemented providers. Others (github, gitlab, s3, …) are recognised as
// names so a missing implementation can be refused clearly.
const (
	ProviderLocal    = "local"
	ProviderGitLocal = "git-local"
	ProviderGitHub   = "github"
	ProviderGitLab   = "gitlab"
	ProviderGitea    = "gitea"
	ProviderGit      = "git"
	ProviderS3       = "s3"
	ProviderGCS      = "gcs"
	ProviderAzure    = "azure"
	ProviderOSS      = "oss"
	ProviderSFTP     = "sftp"
	ProviderSSH      = "ssh"
)

// ListedDocument is one member a Backend.List can register as a document.
type ListedDocument struct {
	Name     string
	Location string
	Path     string
}

// Backend manages one (kind, provider) knowledge base: list members for a
// collection register, and stamp the Source fields a later Sync/Load uses.
// Fetch stays on SourceLoader — local backends still read files.
type Backend interface {
	Kind() string
	Provider() string
	// SourceType is the legacy ResourceSource.Type stamped on documents.
	SourceType() string
	List(ctx context.Context, location string, suffixes []string) ([]ListedDocument, error)
}

type backendKey struct{ kind, provider string }

var implementedBackends = map[backendKey]Backend{
	{KindFilesystem, ProviderLocal}:              filesystemLocal{},
	{KindSourceCodeRepository, ProviderGitLocal}: gitLocal{},
}

// ImplementedBackends is the (kind, provider) pairs this runtime can manage.
func ImplementedBackends() []Backend {
	return []Backend{filesystemLocal{}, gitLocal{}}
}

// ResolveBackend picks the implementation for a register request. Empty
// kind/provider fall back to the legacy source type (local_file →
// filesystem/local, git → source_code_repository/git-local).
func ResolveBackend(kind, provider, sourceType string) (Backend, error) {
	k, p, err := normalizeKnowledge(kind, provider, sourceType)
	if err != nil {
		return nil, err
	}
	if b, ok := implementedBackends[backendKey{k, p}]; ok {
		return b, nil
	}
	return nil, Errorf(ErrInvalidQuery, "knowledge base %s/%s is not implemented (have filesystem/local, source_code_repository/git-local)", k, p)
}

func normalizeKnowledge(kind, provider, sourceType string) (string, string, error) {
	k := strings.ToLower(strings.TrimSpace(kind))
	p := strings.ToLower(strings.TrimSpace(provider))
	t := strings.ToLower(strings.TrimSpace(sourceType))
	if k == "" {
		switch t {
		case "", "local_file":
			k = KindFilesystem
		case "git":
			k = KindSourceCodeRepository
		case "external":
			k = KindCloud
		default:
			return "", "", Errorf(ErrInvalidQuery, "unknown source type %q", sourceType)
		}
	}
	switch k {
	case KindFilesystem:
		if p == "" {
			p = ProviderLocal
		}
	case KindSourceCodeRepository:
		if p == "" {
			p = ProviderGitLocal
		}
	case KindCloud:
		if p == "" {
			return k, "", Errorf(ErrInvalidQuery, "cloud storage needs a provider (s3, gcs, azure, oss); none are implemented yet")
		}
	default:
		return "", "", Errorf(ErrInvalidQuery, "kind must be filesystem, source_code_repository, or cloud")
	}
	return k, p, nil
}

// StampSource writes this backend's kind, provider and legacy type onto src.
func StampSource(src ResourceSource, b Backend) ResourceSource {
	src.Kind = b.Kind()
	src.Provider = b.Provider()
	src.Type = b.SourceType()
	return src
}

func looksRemoteLocation(location string) bool {
	s := strings.ToLower(strings.TrimSpace(location))
	if s == "" {
		return false
	}
	if strings.Contains(s, "://") || strings.HasPrefix(s, "git@") {
		return true
	}
	return false
}

func listedFromFiles(location string, files []string) []ListedDocument {
	out := make([]ListedDocument, 0, len(files))
	for _, rel := range files {
		out = append(out, ListedDocument{Name: rel, Location: location, Path: rel})
	}
	return out
}
