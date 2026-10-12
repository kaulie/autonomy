package context

import (
	"context"
	"strings"
)

// filesystemLocal is filesystem / local: a path tree on the runtime host.
type filesystemLocal struct{}

func (filesystemLocal) Kind() string       { return KindFilesystem }
func (filesystemLocal) Provider() string   { return ProviderLocal }
func (filesystemLocal) SourceType() string { return "local_file" }

func (filesystemLocal) List(_ context.Context, location string, suffixes []string) ([]ListedDocument, error) {
	root := strings.TrimSpace(location)
	if looksRemoteLocation(root) {
		return nil, Errorf(ErrInvalidQuery, "filesystem/local location must be a path on the runtime host")
	}
	files, err := DocumentFiles(root, suffixes)
	if err != nil {
		return nil, err
	}
	return listedFromFiles(root, files), nil
}
