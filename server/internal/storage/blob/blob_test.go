package blob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
)

func newFSStore(t *testing.T) (Store, string) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Blob.Backend = config.BlobFS
	cfg.Blob.BaseDir = filepath.Join(t.TempDir(), "blobs")
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("fs store: %v", err)
	}
	return s, cfg.Blob.BaseDir
}

func newMinIOStore(t *testing.T) (Store, *s3Fake) {
	t.Helper()
	fake, endpoint := newS3Fake(t)
	cfg := &config.Config{}
	cfg.Blob.Backend = config.BlobMinIO
	cfg.Blob.Endpoint = endpoint
	cfg.Blob.AccessKey, cfg.Blob.SecretKey = "test-access", "test-secret"
	cfg.Blob.Bucket = "openbeehive-test"
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("minio store: %v", err)
	}
	return s, fake
}

// onEachBackend runs the test against the filesystem store and against the
// MinIO store talking to the in-process S3 fake.
func onEachBackend(t *testing.T, test func(t *testing.T, s Store)) {
	t.Run("fs", func(t *testing.T) {
		s, _ := newFSStore(t)
		test(t, s)
	})
	t.Run("minio", func(t *testing.T) {
		s, _ := newMinIOStore(t)
		test(t, s)
	})
}

func put(t *testing.T, s Store, key string, data []byte) {
	t.Helper()
	if err := s.Put(context.Background(), key, bytes.NewReader(data), int64(len(data)), "image/jpeg"); err != nil {
		t.Fatalf("put %s: %v", key, err)
	}
}

func get(t *testing.T, s Store, key string) []byte {
	t.Helper()
	rc, size, err := s.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	if size != int64(len(data)) {
		t.Fatalf("get %s: reported size %d, read %d bytes", key, size, len(data))
	}
	return data
}

func keys(t *testing.T, s Store, prefix string) []string {
	t.Helper()
	var out []string
	err := s.List(context.Background(), prefix, func(o Object) error {
		out = append(out, o.Key)
		return nil
	})
	if err != nil {
		t.Fatalf("list %q: %v", prefix, err)
	}
	sort.Strings(out)
	return out
}

func TestPutGetDelete(t *testing.T) {
	onEachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		key := PhotoKey("org-a", "photo-1")
		// Larger than one read buffer, and not text.
		data := bytes.Repeat([]byte{0xff, 0xd8, 0x00, 0x7f, '\r', '\n'}, 50_000)
		put(t, s, key, data)
		if got := get(t, s, key); !bytes.Equal(got, data) {
			t.Fatalf("read back %d bytes, want %d identical bytes", len(got), len(data))
		}

		// Put replaces.
		put(t, s, key, []byte("second"))
		if got := get(t, s, key); string(got) != "second" {
			t.Fatalf("after replacing: %q", got)
		}

		if err := s.Delete(ctx, key); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, _, err := s.Get(ctx, key); !errors.Is(err, ErrNotFound) {
			t.Fatalf("get after delete: %v, want ErrNotFound", err)
		}
		if err := s.Delete(ctx, key); err != nil {
			t.Fatalf("deleting a missing key must not fail: %v", err)
		}
	})
}

func TestListAndDeletePrefix(t *testing.T) {
	onEachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		a1, a2 := PhotoKey("org-a", "p1"), PhotoKey("org-a", "p2")
		b1 := PhotoKey("org-b", "p1")
		// A workspace whose id starts like another's must not be caught by its prefix.
		ab := PhotoKey("org-ab", "p1")
		for _, k := range []string{a1, a2, b1, ab} {
			put(t, s, k, []byte(k))
		}

		all := keys(t, s, "")
		if len(all) != 4 {
			t.Fatalf("list all: %v", all)
		}
		want := []string{a1, a2}
		sort.Strings(want)
		if got := keys(t, s, WorkspacePrefix("org-a")); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("list org-a: %v, want %v", got, want)
		}
		err := s.List(ctx, "", func(o Object) error {
			if o.Size != int64(len(o.Key)) || time.Since(o.ModTime) > time.Minute || o.ModTime.IsZero() {
				t.Errorf("object %s: size %d, modified %v", o.Key, o.Size, o.ModTime)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		n, err := s.DeletePrefix(ctx, WorkspacePrefix("org-a"))
		if err != nil || n != 2 {
			t.Fatalf("delete prefix: removed %d, err %v; want 2", n, err)
		}
		left := keys(t, s, "")
		wantLeft := []string{b1, ab}
		sort.Strings(wantLeft)
		if strings.Join(left, ",") != strings.Join(wantLeft, ",") {
			t.Fatalf("left after purge: %v, want %v", left, wantLeft)
		}
		// Purging a workspace that stores nothing is fine.
		if n, err := s.DeletePrefix(ctx, WorkspacePrefix("org-none")); err != nil || n != 0 {
			t.Fatalf("empty purge: %d, %v", n, err)
		}
	})
}

func TestListStopsOnCallbackError(t *testing.T) {
	onEachBackend(t, func(t *testing.T, s Store) {
		put(t, s, PhotoKey("org-a", "p1"), []byte("1"))
		put(t, s, PhotoKey("org-a", "p2"), []byte("2"))
		stop := errors.New("stop")
		calls := 0
		err := s.List(context.Background(), "", func(Object) error { calls++; return stop })
		if !errors.Is(err, stop) || calls != 1 {
			t.Fatalf("list: err %v after %d calls; want the callback's error after 1", err, calls)
		}
	})
}

// Keys are built by the server, but the stores must still refuse anything
// that could leave the store or address it as a whole.
func TestInvalidKeysAreRefused(t *testing.T) {
	bad := []string{
		"", "/", "/etc/passwd", "../x", "a/../b", "a/./b", "a//b", "a/", "/a", ".hidden", "a/.upload-1",
		`a\b`, "a b", "a\x00b", "ä", "a/%2e%2e/b", "..", ".", strings.Repeat("a", 300),
		strings.Repeat("a", 129) + "/b",
	}
	onEachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		for _, key := range bad {
			if ValidKey(key) {
				t.Errorf("ValidKey(%q) = true", key)
			}
			if err := s.Put(ctx, key, strings.NewReader("x"), 1, ""); !errors.Is(err, ErrInvalidKey) {
				t.Errorf("put %q: %v, want ErrInvalidKey", key, err)
			}
			if _, _, err := s.Get(ctx, key); !errors.Is(err, ErrInvalidKey) {
				t.Errorf("get %q: %v, want ErrInvalidKey", key, err)
			}
			if err := s.Delete(ctx, key); !errors.Is(err, ErrInvalidKey) {
				t.Errorf("delete %q: %v, want ErrInvalidKey", key, err)
			}
		}
		// A prefix must name a whole directory; "" would be the entire store.
		for _, prefix := range []string{"", "/", "org-a", "../", "a/../", "./", "//"} {
			if _, err := s.DeletePrefix(ctx, prefix); !errors.Is(err, ErrInvalidKey) {
				t.Errorf("delete prefix %q: %v, want ErrInvalidKey", prefix, err)
			}
		}
		for _, prefix := range []string{"org-a", "../", "a/../"} {
			if err := s.List(ctx, prefix, func(Object) error { return nil }); !errors.Is(err, ErrInvalidKey) {
				t.Errorf("list %q: %v, want ErrInvalidKey", prefix, err)
			}
		}
	})
}

func TestKeysAreWorkspacePrefixedAndSafe(t *testing.T) {
	for _, org := range []string{"local", "0b7c6d5e-1111-4222-8333-444455556666", "../../etc", "a/b", "", "org with space", strings.Repeat("x", 200)} {
		prefix := WorkspacePrefix(org)
		key := PhotoKey(org, "../../photo\x00id")
		if !strings.HasPrefix(key, prefix) || !ValidKey(key) || !validPrefix(prefix) {
			t.Errorf("org %q: key %q, prefix %q", org, key, prefix)
		}
	}
	if WorkspacePrefix("local") != "local/" {
		t.Errorf("a safe workspace id is used as it is, got %q", WorkspacePrefix("local"))
	}
	if PhotoKey("a", "1") == PhotoKey("a", "2") || PhotoKey("a", "1") == PhotoKey("b", "1") {
		t.Error("keys must differ per workspace and per photo")
	}
	if WorkspacePrefix("a/b") == WorkspacePrefix("a_b") {
		t.Error("unsafe ids must not collapse onto safe ones")
	}
}

// The filesystem store never writes outside its directory and never leaves a
// partly written object where a reader could find it.
func TestFSWritesStayInsideAndAreAtomic(t *testing.T) {
	s, base := newFSStore(t)
	ctx := context.Background()
	key := PhotoKey("org-a", "p1")

	// A body that ends early: nothing may appear under the key.
	err := s.Put(ctx, key, strings.NewReader("short"), 100, "image/jpeg")
	if err == nil {
		t.Fatal("a truncated upload must fail")
	}
	if _, _, err := s.Get(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("truncated upload left an object: %v", err)
	}
	if got := keys(t, s, ""); len(got) != 0 {
		t.Fatalf("truncated upload left files: %v", got)
	}
	// Nor a temporary file.
	var files []string
	_ = filepath.WalkDir(base, func(path string, d os.DirEntry, _ error) error {
		if d != nil && !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if len(files) != 0 {
		t.Fatalf("files left behind: %v", files)
	}

	put(t, s, key, []byte("data"))
	want := filepath.Join(base, filepath.FromSlash(key))
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("object not at %s: %v", want, err)
	}
	// A directory is not an object.
	if _, _, err := s.Get(ctx, "org-a/photos"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get of a directory: %v", err)
	}
}

// What a crashed upload leaves behind is not listed, and is cleaned up once
// it is old enough to be certainly abandoned.
func TestFSListSkipsAndCleansTemporaryFiles(t *testing.T) {
	s, base := newFSStore(t)
	key := PhotoKey("org-a", "p1")
	put(t, s, key, []byte("data"))
	dir := filepath.Dir(filepath.Join(base, filepath.FromSlash(key)))
	fresh := filepath.Join(dir, tmpPrefix+"fresh")
	stale := filepath.Join(dir, tmpPrefix+"stale")
	for _, f := range []string{fresh, stale} {
		if err := os.WriteFile(f, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * staleTmpAge)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if got := keys(t, s, ""); len(got) != 1 || got[0] != key {
		t.Fatalf("list: %v, want only %s", got, key)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("an upload in flight must be left alone: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("an abandoned temporary file must be removed, stat: %v", err)
	}
}

// The MinIO backend creates its bucket and stores the content type.
func TestMinIOCreatesBucketAndSetsContentType(t *testing.T) {
	s, fake := newMinIOStore(t)
	put(t, s, PhotoKey("org-a", "p1"), []byte("data"))
	fake.mu.Lock()
	defer fake.mu.Unlock()
	objects, ok := fake.buckets["openbeehive-test"]
	if !ok {
		t.Fatal("bucket was not created")
	}
	o, ok := objects[PhotoKey("org-a", "p1")]
	if !ok || string(o.data) != "data" || o.contentType != "image/jpeg" {
		t.Fatalf("stored object: %+v", o)
	}
}

func TestUnknownBackend(t *testing.T) {
	cfg := &config.Config{}
	cfg.Blob.Backend = "tape"
	if _, err := New(cfg); err == nil {
		t.Fatal("an unknown backend must be refused")
	}
}

// The blob directory must not hold the SQLite database: the photo sweep
// lists and deletes below it.
func TestFSRefusesTheDatabaseDirectory(t *testing.T) {
	dir := t.TempDir()
	for dsn, refused := range map[string]bool{
		"file:" + filepath.Join(dir, "openbeehive.db") + "?_pragma=busy_timeout(5000)": true,
		filepath.Join(dir, "sub", "x.db"):                                              true,
		"file:" + filepath.Join(dir, "..", "elsewhere.db"):                             false,
		"file::memory:?cache=shared":                                                   false,
	} {
		cfg := &config.Config{}
		cfg.Blob.Backend = config.BlobFS
		cfg.Blob.BaseDir = dir
		cfg.DB.Driver = config.DriverSQLite
		cfg.DB.DSN = dsn
		_, err := New(cfg)
		if refused != (err != nil) {
			t.Errorf("DSN %s: err %v, want refused %v", dsn, err, refused)
		}
		if err != nil && !strings.Contains(err.Error(), "BEEHIVE_BLOB_DIR") {
			t.Errorf("error does not name the setting: %v", err)
		}
	}
	// A database elsewhere, or not SQLite, is fine.
	cfg := &config.Config{}
	cfg.Blob.Backend = config.BlobFS
	cfg.Blob.BaseDir = filepath.Join(dir, "blobs")
	cfg.DB.Driver = config.DriverSQLite
	cfg.DB.DSN = "file:" + filepath.Join(dir, "openbeehive.db")
	if _, err := New(cfg); err != nil {
		t.Fatalf("blobs next to the database: %v", err)
	}
}

func TestIsPhotoKey(t *testing.T) {
	for key, want := range map[string]bool{
		PhotoKey("org-a", "p1"):                   true,
		PhotoKey("weird id with spaces", "p"):     true,
		"openbeehive.db":                          false,
		"org-a/photos/abc":                        false,
		"org-a/photos/" + strings.Repeat("A", 64): false,
		"org-a/other/" + strings.Repeat("a", 64):  false,
		"a/b/photos/" + strings.Repeat("a", 64):   false,
	} {
		if got := IsPhotoKey(key); got != want {
			t.Errorf("IsPhotoKey(%q) = %v, want %v", key, got, want)
		}
	}
}
