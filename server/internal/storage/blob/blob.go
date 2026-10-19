// Package blob abstracts the object store.
// Implementations: MinIO/S3 (cloud) and local filesystem (selfhost).
package blob

import (
	"context"
	"fmt"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
)

// Store is the object-store surface the server uses. Photos are addressed by
// the keys synced in inspection.photo_keys; the server only deletes them
// (tenant purge). Self-host serves FS blobs from /files/ directly.
type Store interface {
	Delete(ctx context.Context, key string) error
}

func New(cfg *config.Config) (Store, error) {
	switch cfg.Blob.Backend {
	case config.BlobMinIO:
		return newMinIO(cfg)
	case config.BlobFS:
		return newFS(cfg)
	default:
		return nil, fmt.Errorf("unknown blob backend %q", cfg.Blob.Backend)
	}
}
