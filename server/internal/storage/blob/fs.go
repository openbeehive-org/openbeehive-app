package blob

import (
	"context"
	"os"
	"path/filepath"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
)

// fsStore keeps blobs as plain files under the configured base directory.
type fsStore struct {
	base string
}

func newFS(cfg *config.Config) (Store, error) {
	if err := os.MkdirAll(cfg.Blob.BaseDir, 0o755); err != nil {
		return nil, err
	}
	return &fsStore{base: cfg.Blob.BaseDir}, nil
}

func (f *fsStore) path(key string) string {
	// key may contain "/" -> subfolders. Keys are client-supplied (synced
	// photo_keys), so root-anchor + Clean before joining: "../" can then never
	// escape the base directory.
	return filepath.Join(f.base, filepath.Clean(string(filepath.Separator)+filepath.FromSlash(key)))
}

func (f *fsStore) Delete(ctx context.Context, key string) error {
	return os.Remove(f.path(key))
}
