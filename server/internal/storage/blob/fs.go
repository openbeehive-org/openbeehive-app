package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
)

// fsStore keeps blobs as plain files under the configured base directory:
// the key is the path below it.
type fsStore struct {
	base string
}

// tmpPrefix marks files that are still being written. Keys never contain a
// segment that starts with a dot, so such a file is never an object.
const tmpPrefix = ".upload-"

// staleTmpAge is how old a temporary file must be before List removes it:
// an upload that takes longer than this has been abandoned.
const staleTmpAge = time.Hour

func newFS(cfg *config.Config) (Store, error) {
	base, err := filepath.Abs(cfg.Blob.BaseDir)
	if err != nil {
		return nil, err
	}
	if cfg.DB.Driver == config.DriverSQLite {
		if db := sqliteFile(cfg.DB.DSN); db != "" && within(base, db) {
			return nil, fmt.Errorf("BEEHIVE_BLOB_DIR (%s) contains the database file %s: "+
				"give the blob store a directory of its own, e.g. BEEHIVE_BLOB_DIR=%s",
				base, db, filepath.Join(filepath.Dir(db), "blobs"))
		}
	}
	if err := os.MkdirAll(base, 0o750); err != nil {
		return nil, err
	}
	return &fsStore{base: base}, nil
}

// sqliteFile returns the absolute path of the database file a SQLite DSN
// names ("file:x.db?..." or a plain path), or "" for an in-memory database.
func sqliteFile(dsn string) string {
	path := strings.TrimPrefix(dsn, "file:")
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	if path == "" || path == ":memory:" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	return abs
}

// within reports whether path lies inside dir. The store's clean-up lists
// and deletes below its base directory, so it must not hold other files.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// path maps a key to its file. Only valid keys get a path: their segments
// cannot be "." or "..", absolute, or contain a separator, so the result is
// always below the base directory.
func (f *fsStore) path(key string) (string, error) {
	if !ValidKey(key) {
		return "", ErrInvalidKey
	}
	return filepath.Join(f.base, filepath.FromSlash(key)), nil
}

// Put writes to a temporary file in the target directory and renames it
// into place, so a reader sees the old object or the complete new one and a
// crash leaves no half-written object behind.
func (f *fsStore) Put(_ context.Context, key string, r io.Reader, size int64, _ string) error {
	path, err := f.path(key)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, tmpPrefix+"*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // a no-op once renamed
	n, err := io.Copy(tmp, r)
	if err == nil && size >= 0 && n != size {
		err = io.ErrUnexpectedEOF
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (f *fsStore) Get(_ context.Context, key string) (io.ReadCloser, int64, error) {
	path, err := f.path(key)
	if err != nil {
		return nil, 0, err
	}
	file, err := os.Open(path) //nolint:gosec // G304: path is built from a validated key below the base directory
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	info, err := file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = ErrNotFound
	}
	if err != nil {
		_ = file.Close()
		return nil, 0, err
	}
	return file, info.Size(), nil
}

func (f *fsStore) Delete(_ context.Context, key string) error {
	path, err := f.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (f *fsStore) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	if !validPrefix(prefix) {
		return 0, ErrInvalidKey
	}
	n := 0
	if err := f.List(ctx, prefix, func(Object) error { n++; return nil }); err != nil {
		return 0, err
	}
	dir := filepath.Join(f.base, filepath.FromSlash(strings.TrimSuffix(prefix, "/")))
	return n, os.RemoveAll(dir)
}

func (f *fsStore) List(ctx context.Context, prefix string, fn func(Object) error) error {
	if prefix != "" && !validPrefix(prefix) {
		return ErrInvalidKey
	}
	root := filepath.Join(f.base, filepath.FromSlash(strings.TrimSuffix(prefix, "/")))
	var abandoned []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil // removed while listing
		}
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") {
			// Not an object. An old temporary file is what a crashed upload left.
			if strings.HasPrefix(d.Name(), tmpPrefix) && time.Since(info.ModTime()) > staleTmpAge {
				abandoned = append(abandoned, path)
			}
			return nil
		}
		return fn(Object{Key: f.key(path), Size: info.Size(), ModTime: info.ModTime()})
	})
	for _, path := range abandoned {
		_ = os.Remove(path)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil // nothing stored under this prefix yet
	}
	return err
}

// key is the inverse of path, for files found below the base directory.
func (f *fsStore) key(path string) string {
	rel, err := filepath.Rel(f.base, path)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}
