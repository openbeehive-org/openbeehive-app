package blob

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// s3Fake is an in-process stand-in for the part of the S3 API the MinIO
// backend uses: bucket existence and creation, and put, get, head, delete
// and list (v2) of objects, addressed path-style. Signatures are not
// checked.
type s3Fake struct {
	mu      sync.Mutex
	buckets map[string]map[string]s3Object
	// requests counts handled requests per "METHOD kind", for assertions.
	requests map[string]int
}

type s3Object struct {
	data        []byte
	contentType string
	modTime     time.Time
}

func newS3Fake(t *testing.T) (*s3Fake, string) {
	t.Helper()
	f := &s3Fake{buckets: map[string]map[string]s3Object{}, requests: map[string]int{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, strings.TrimPrefix(srv.URL, "http://")
}

func (f *s3Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	f.mu.Lock()
	defer f.mu.Unlock()
	if key == "" {
		f.requests[r.Method+" bucket"]++
		f.serveBucket(w, r, bucket)
		return
	}
	f.requests[r.Method+" object"]++
	objects, ok := f.buckets[bucket]
	if !ok {
		s3Error(w, http.StatusNotFound, "NoSuchBucket")
		return
	}
	switch r.Method {
	case http.MethodPut:
		data, err := readS3Body(r)
		if err != nil {
			s3Error(w, http.StatusBadRequest, "IncompleteBody")
			return
		}
		objects[key] = s3Object{data: data, contentType: r.Header.Get("Content-Type"), modTime: time.Now()}
		w.Header().Set("ETag", `"fake"`)
	case http.MethodGet, http.MethodHead:
		o, ok := objects[key]
		if !ok {
			s3Error(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		w.Header().Set("Content-Type", o.contentType)
		w.Header().Set("ETag", `"fake"`)
		http.ServeContent(w, r, "", o.modTime, bytes.NewReader(o.data))
	case http.MethodDelete:
		delete(objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		s3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed")
	}
}

func (f *s3Fake) serveBucket(w http.ResponseWriter, r *http.Request, bucket string) {
	objects, exists := f.buckets[bucket]
	q := r.URL.Query()
	switch {
	case r.Method == http.MethodPut:
		if !exists {
			f.buckets[bucket] = map[string]s3Object{}
		}
	case !exists:
		s3Error(w, http.StatusNotFound, "NoSuchBucket")
	case r.Method == http.MethodHead:
	case q.Has("location"):
		_, _ = io.WriteString(w, `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
	case q.Get("list-type") == "2":
		writeS3Listing(w, bucket, q.Get("prefix"), objects)
	default:
		s3Error(w, http.StatusNotImplemented, "NotImplemented")
	}
}

func writeS3Listing(w http.ResponseWriter, bucket, prefix string, objects map[string]s3Object) {
	type content struct {
		Key          string
		LastModified string
		Size         int
	}
	out := struct {
		XMLName     xml.Name `xml:"ListBucketResult"`
		Name        string
		Prefix      string
		KeyCount    int
		IsTruncated bool
		Contents    []content
	}{Name: bucket, Prefix: prefix}
	for key, o := range objects {
		if strings.HasPrefix(key, prefix) {
			out.Contents = append(out.Contents, content{
				Key: key, LastModified: o.modTime.UTC().Format("2006-01-02T15:04:05.000Z"), Size: len(o.data),
			})
		}
	}
	sort.Slice(out.Contents, func(i, j int) bool { return out.Contents[i].Key < out.Contents[j].Key })
	out.KeyCount = len(out.Contents)
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(out)
}

func s3Error(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<Error><Code>%s</Code><Message>%s</Message></Error>`, code, code)
}

// readS3Body returns the object bytes of a PUT. Over plain HTTP the client
// signs the body in chunks ("aws-chunked": "<hex size>;chunk-signature=..",
// CRLF, data, CRLF, ..., a zero-size chunk, optional trailers).
func readS3Body(r *http.Request) ([]byte, error) {
	if !strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-") {
		return io.ReadAll(r.Body)
	}
	br := bufio.NewReader(r.Body)
	var out bytes.Buffer
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		sizeHex, _, _ := strings.Cut(strings.TrimSpace(line), ";")
		size, err := strconv.ParseInt(sizeHex, 16, 64)
		if err != nil {
			return nil, err
		}
		if size == 0 {
			return out.Bytes(), nil
		}
		if _, err := io.CopyN(&out, br, size); err != nil {
			return nil, err
		}
		if _, err := br.Discard(2); err != nil { // the CRLF after the chunk
			return nil, err
		}
	}
}
