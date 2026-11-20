package blob

import (
	"context"
	"io"
	"net/http"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
)

// minioStore keeps blobs in one bucket of an S3-compatible service.
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

func (m *minioStore) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	if !ValidKey(key) {
		return ErrInvalidKey
	}
	_, err := m.c.PutObject(ctx, m.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	return err
}

func (m *minioStore) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	if !ValidKey(key) {
		return nil, 0, ErrInvalidKey
	}
	obj, err := m.c.GetObject(ctx, m.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, err
	}
	// GetObject is lazy: Stat sends the request and reports a missing key.
	info, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		if minio.ToErrorResponse(err).StatusCode == http.StatusNotFound {
			return nil, 0, ErrNotFound
		}
		return nil, 0, err
	}
	return obj, info.Size, nil
}

func (m *minioStore) Delete(ctx context.Context, key string) error {
	if !ValidKey(key) {
		return ErrInvalidKey
	}
	return m.c.RemoveObject(ctx, m.bucket, key, minio.RemoveObjectOptions{})
}

func (m *minioStore) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	if !validPrefix(prefix) {
		return 0, ErrInvalidKey
	}
	n := 0
	err := m.List(ctx, prefix, func(o Object) error {
		if err := m.c.RemoveObject(ctx, m.bucket, o.Key, minio.RemoveObjectOptions{}); err != nil {
			return err
		}
		n++
		return nil
	})
	return n, err
}

func (m *minioStore) List(ctx context.Context, prefix string, fn func(Object) error) error {
	if prefix != "" && !validPrefix(prefix) {
		return ErrInvalidKey
	}
	// Cancelling stops the listing goroutine when fn ends the walk early.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for o := range m.c.ListObjects(ctx, m.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if o.Err != nil {
			return o.Err
		}
		if err := fn(Object{Key: o.Key, Size: o.Size, ModTime: o.LastModified}); err != nil {
			return err
		}
	}
	return nil
}
