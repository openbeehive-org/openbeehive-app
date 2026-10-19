package blob

import (
	"context"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
)

type minioStore struct {
	c      *minio.Client
	bucket string
}

func newMinIO(cfg *config.Config) (Store, error) {
	c, err := minio.New(cfg.Blob.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.Blob.AccessKey, cfg.Blob.SecretKey, ""),
		Secure: cfg.Blob.UseSSL,
	})
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	exists, err := c.BucketExists(ctx, cfg.Blob.Bucket)
	if err != nil {
		return nil, err
	}
	if !exists {
		if err := c.MakeBucket(ctx, cfg.Blob.Bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, err
		}
	}
	return &minioStore{c: c, bucket: cfg.Blob.Bucket}, nil
}

func (m *minioStore) Delete(ctx context.Context, key string) error {
	return m.c.RemoveObject(ctx, m.bucket, key, minio.RemoveObjectOptions{})
}
