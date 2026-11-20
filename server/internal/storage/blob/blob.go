// Package blob abstracts the object store that holds full-size photos.
// Implementations: MinIO/S3 (cloud) and local filesystem (selfhost).
//
// Keys are built by the server (PhotoKey), never taken from a client: the
// first segment is the workspace, so one workspace's objects can be listed
// and purged by prefix, and no segment can address anything outside the
// store. Objects are only reachable through the authenticated photo
// endpoints; neither backend is ever exposed directly.
package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
)

var (
	// ErrNotFound is returned by Get when no object is stored under the key.
	ErrNotFound = errors.New("blob not found")
	// ErrInvalidKey is returned for keys and prefixes that are not of the
	// form the server itself builds.
	ErrInvalidKey = errors.New("invalid blob key")
)

// Object describes one stored object.
type Object struct {
	Key     string
	Size    int64
	ModTime time.Time
}

// Store is the object-store surface the server uses.
type Store interface {
	// Put stores size bytes read from r under key, replacing what was there.
	// The object becomes visible only once it is complete.
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// Get opens the object and reports its size, or returns ErrNotFound.
	Get(ctx context.Context, key string) (io.ReadCloser, int64, error)
	// Delete removes the object. A key that holds nothing is not an error.
	Delete(ctx context.Context, key string) error
	// DeletePrefix removes every object whose key starts with prefix, which
	// must end in "/", and returns how many it removed.
	DeletePrefix(ctx context.Context, prefix string) (int, error)
	// List calls fn for every object whose key starts with prefix ("" lists
	// the whole store), in no particular order.
	List(ctx context.Context, prefix string, fn func(Object) error) error
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

const maxKeyLen = 255

// A key segment starts with a letter, digit, "_" or "-": never with a dot,
// which rules out "." and ".." and leaves dot-files to the filesystem
// backend for its temporary files.
var keySegment = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]{0,127}$`)

// ValidKey reports whether key is a "/"-separated list of safe segments.
func ValidKey(key string) bool {
	if key == "" || len(key) > maxKeyLen {
		return false
	}
	for _, seg := range strings.Split(key, "/") {
		if !keySegment.MatchString(seg) {
			return false
		}
	}
	return true
}

// validPrefix reports whether prefix names a whole "directory": a valid key
// followed by "/". An empty prefix is not valid here.
func validPrefix(prefix string) bool {
	return strings.HasSuffix(prefix, "/") && ValidKey(strings.TrimSuffix(prefix, "/"))
}

func hashed(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// WorkspacePrefix is the key prefix (ending in "/") under which everything
// of one workspace is stored. Workspace ids are used as they are when they
// are safe segments (the server issues UUIDs); any other id is replaced by
// its hash.
func WorkspacePrefix(orgID string) string {
	if len(orgID) <= 64 && keySegment.MatchString(orgID) {
		return orgID + "/"
	}
	return "x" + hashed(orgID)[:40] + "/"
}

// photoKey matches the keys PhotoKey builds.
var photoKey = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]{0,127}/photos/[0-9a-f]{64}$`)

// IsPhotoKey reports whether key has the form of a key PhotoKey builds.
// Clean-up passes over the whole store touch nothing else, in case the
// store is shared with other files.
func IsPhotoKey(key string) bool {
	return photoKey.MatchString(key)
}

// PhotoKey is the key of a photo's full-size image. Photo ids are chosen by
// devices, so the key carries their hash instead of the id itself.
func PhotoKey(orgID, photoID string) string {
	return WorkspacePrefix(orgID) + "photos/" + hashed(photoID)
}
