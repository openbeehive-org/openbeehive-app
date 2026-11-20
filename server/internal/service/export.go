package service

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage/blob"
)

// Server-side export of a workspace:
//
//	GET /export            the records as one JSON document
//	GET /export?photos=1   a ZIP of that document plus the full-size images
//
// The format is the app's own export (app/src/lib/data/backup.ts), so the
// app imports either. It holds the live records of the caller's active
// workspace, with the caller's personal tasks but no other member's. Rows
// are read in pages and written as they come, so memory stays flat however
// large the workspace is.
//
// Every member may download it except the demo account and read-only API
// keys. Like the photo endpoints it accepts the session or an API key, and
// refuses cross-site browser requests: a page elsewhere cannot make the
// browser fetch it with the session cookie (it could not read the answer
// anyway; this also keeps it from using up the caller's allowance).

const (
	// ExportPath is where the export is served.
	ExportPath = "/export"
	// exportDocument is the document's name inside the ZIP.
	exportDocument = "openbeehive-export.json"
	// exportFormat is the version of the document layout (see backup.ts).
	exportFormat = 2
	// exportsPerHour is how many exports one user may start per hour.
	exportsPerHour = 5
	// exportPage is how many rows one query reads.
	exportPage = 500
	// exportWriteTime bounds how long one download may take.
	exportWriteTime = 30 * time.Minute
)

// exportEntities is the order in which the document lists the tables: what
// is referred to comes before what refers to it.
var exportEntities = []string{"apiary", "hive", "queen", "placement", "inspection", "harvest", "treatment", "task", "event"}

// Exporter serves ExportPath.
type Exporter struct {
	db    *sqlx.DB
	blobs blob.Store
	limit *exportLimiter
	now   func() time.Time
}

// NewExporter returns the export endpoint. blobs may be nil: the ZIP then
// lists every photo as missing.
func NewExporter(db *sqlx.DB, blobs blob.Store) *Exporter {
	return &Exporter{db: db, blobs: blobs, limit: newExportLimiter(exportsPerHour, time.Hour), now: time.Now}
}

// Handler serves the export. identify authenticates like the photo
// endpoints; sameSite is the cross-site rule (nil allows all).
func (e *Exporter) Handler(identify PhotoIdentify, sameSite func(*http.Request) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != ExportPath {
			photoError(w, http.StatusNotFound, "not_found", "unknown endpoint")
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			photoError(w, http.StatusMethodNotAllowed, "method_not_allowed", "this endpoint requires GET")
			return
		}
		if sameSite != nil && !sameSite(r) {
			photoError(w, http.StatusForbidden, "cross_site_request", "cross-site requests are not allowed")
			return
		}
		id, status := identify(r)
		switch status {
		case 0:
		case http.StatusServiceUnavailable:
			photoError(w, status, "unavailable", "the session could not be verified right now; try again")
			return
		default:
			photoError(w, http.StatusUnauthorized, "not_signed_in", "not signed in")
			return
		}
		switch {
		case id.Role == "demo":
			photoError(w, http.StatusForbidden, "demo_read_only", "the demo account cannot export")
			return
		case id.ReadOnly:
			photoError(w, http.StatusForbidden, "read_only", "read-only API keys cannot export")
			return
		}
		if wait := e.limit.take(id.UserID, e.now()); wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			photoError(w, http.StatusTooManyRequests, "rate_limited",
				fmt.Sprintf("at most %d exports per hour; try again later", exportsPerHour))
			return
		}
		e.serve(w, r, id, r.URL.Query().Get("photos") == "1" || r.URL.Query().Get("photos") == "true")
	})
}

func (e *Exporter) serve(w http.ResponseWriter, r *http.Request, id auth.Identity, photos bool) {
	// A large workspace takes longer than the server's write timeout.
	_ = http.NewResponseController(w).SetWriteDeadline(e.now().Add(exportWriteTime))
	ext, typ := "json", "application/json"
	if photos {
		ext, typ = "zip", "application/zip"
	}
	h := w.Header()
	h.Set("Content-Type", typ)
	h.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="openbeehive-server-%s.%s"`, e.now().UTC().Format("2006-01-02"), ext))
	h.Set("Cache-Control", "no-store")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")

	var err error
	if photos {
		err = e.writeArchive(r.Context(), w, id)
	} else {
		err = e.writeDocument(r.Context(), w, id, false, nil)
	}
	// The status is sent with the first bytes; a failure after that can only
	// cut the download short, which the client notices as a broken file.
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("export: workspace %s: %v", id.OrgID, err)
	}
}

// writeArchive writes the ZIP: the images first (stored, they are
// compressed already), then the document, which lists the photos whose
// image is missing.
func (e *Exporter) writeArchive(ctx context.Context, w io.Writer, id auth.Identity) error {
	zw := zip.NewWriter(w)
	var missing []string
	err := e.eachPhoto(ctx, id.OrgID, func(photoID, key, typ string) error {
		if key == "" || e.blobs == nil {
			missing = append(missing, photoID)
			return nil
		}
		body, _, err := e.blobs.Get(ctx, key)
		if errors.Is(err, blob.ErrNotFound) {
			missing = append(missing, photoID)
			return nil
		}
		if err != nil {
			return fmt.Errorf("read image of photo %s: %w", photoID, err)
		}
		defer func() { _ = body.Close() }()
		f, err := zw.CreateHeader(&zip.FileHeader{
			Name:     "photos/" + url.PathEscape(photoID) + extensionOf(typ),
			Method:   zip.Store,
			Modified: e.now(),
		})
		if err != nil {
			return err
		}
		_, err = io.Copy(f, body)
		return err
	})
	if err != nil {
		return err
	}
	f, err := zw.CreateHeader(&zip.FileHeader{Name: exportDocument, Method: zip.Deflate, Modified: e.now()})
	if err != nil {
		return err
	}
	if err := e.writeDocument(ctx, f, id, true, missing); err != nil {
		return err
	}
	return zw.Close()
}

// eachPhoto calls fn for every live photo of the workspace, by id.
func (e *Exporter) eachPhoto(ctx context.Context, orgID string, fn func(id, blobKey, blobType string) error) error {
	after := ""
	for {
		var page []struct {
			ID       string `db:"id"`
			BlobKey  string `db:"blob_key"`
			BlobType string `db:"blob_type"`
		}
		err := e.db.SelectContext(ctx, &page, e.db.Rebind(`SELECT id, COALESCE(blob_key, '') AS blob_key, COALESCE(blob_type, '') AS blob_type
			FROM photo WHERE organization_id = ? AND deleted = FALSE AND id > ? ORDER BY id ASC LIMIT ?`), orgID, after, exportPage)
		if err != nil {
			return err
		}
		for _, p := range page {
			if err := fn(p.ID, p.BlobKey, p.BlobType); err != nil {
				return err
			}
			after = p.ID
		}
		if len(page) < exportPage {
			return nil
		}
	}
}

// writeDocument writes the JSON document, table by table.
func (e *Exporter) writeDocument(ctx context.Context, out io.Writer, id auth.Identity, photos bool, missing []string) error {
	w := bufio.NewWriterSize(out, 64<<10)
	head := []struct {
		key   string
		value any
	}{
		{"openbeehive_export", true},
		{"format", exportFormat},
		{"schema_version", "0.5.0"},
		{"exported_at", e.now().UTC().Format("2006-01-02T15:04:05.000Z")},
		{"workspace_id", id.OrgID},
		{"source", "server"},
	}
	if missing != nil {
		head = append(head, struct {
			key   string
			value any
		}{"missing_photos", missing})
	}
	_, _ = w.WriteString("{")
	for _, f := range head {
		v, _ := json.Marshal(f.value)
		_, _ = fmt.Fprintf(w, "%q:%s,", f.key, v)
	}
	_, _ = w.WriteString(`"data":{`)
	entities := exportEntities
	if photos {
		entities = append(append([]string{}, entities...), "photo")
	}
	for i, entity := range entities {
		if i > 0 {
			_, _ = w.WriteString(",")
		}
		_, _ = fmt.Fprintf(w, "%q:[", entity)
		if err := e.writeRows(ctx, w, id, entity); err != nil {
			return err
		}
		_, _ = w.WriteString("]")
	}
	_, _ = w.WriteString("}}\n")
	return w.Flush()
}

// exportColumns are the columns of an entity the document carries: what
// syncs, without the bookkeeping and the inline photos of 0.4.x.
func exportColumns(entity string) []string {
	var cols []string
	for _, c := range entityCols[entity].cols {
		if c == "deleted" || (entity == "hive" && c == "photo") {
			continue
		}
		cols = append(cols, c)
	}
	return cols
}

// writeRows writes the live rows of one table the caller may see, as JSON
// objects separated by commas.
func (e *Exporter) writeRows(ctx context.Context, w *bufio.Writer, id auth.Identity, entity string) error {
	spec := entityCols[entity]
	kinds := kindsOf(entity)
	cols := exportColumns(entity)
	visible, extra := visibleRows(entity, id)
	q := e.db.Rebind(fmt.Sprintf(`SELECT %s FROM %s WHERE organization_id = ? AND deleted = FALSE AND id > ?%s ORDER BY id ASC LIMIT ?`,
		strings.Join(selectList(e.db.DriverName(), kinds, cols), ", "), spec.table, visible))
	after, first := "", true
	for {
		args := append(append([]any{id.OrgID, after}, extra...), exportPage)
		n, last, err := e.writePage(ctx, w, q, args, cols, kinds, &first)
		if err != nil {
			return fmt.Errorf("%s: %w", entity, err)
		}
		if n < exportPage {
			return nil
		}
		after = last
	}
}

func (e *Exporter) writePage(ctx context.Context, w *bufio.Writer, q string, args []any, cols []string, kinds map[string]colKind, first *bool) (n int, last string, err error) {
	rows, err := e.db.QueryxContext(ctx, q, args...)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		m := map[string]any{}
		if err := rows.MapScan(m); err != nil {
			return n, last, err
		}
		obj := make(map[string]any, len(cols))
		for _, c := range cols {
			obj[c] = wireValue(kinds[c], m[c])
		}
		b, err := json.Marshal(obj)
		if err != nil {
			return n, last, err
		}
		if !*first {
			_ = w.WriteByte(',')
		}
		*first = false
		if _, err := w.Write(b); err != nil {
			return n, last, err
		}
		n++
		last = rs(m["id"])
	}
	return n, last, rows.Err()
}

// exportLimiter allows each user a number of exports per sliding window.
// In memory, like the sign-in throttle: one server instance.
type exportLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	starts map[string][]time.Time
}

func newExportLimiter(limit int, window time.Duration) *exportLimiter {
	return &exportLimiter{max: limit, window: window, starts: map[string][]time.Time{}}
}

// take records an export for key and returns 0, or how long until one more
// is allowed (nothing is recorded then).
func (l *exportLimiter) take(key string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.starts[key][:0]
	for _, t := range l.starts[key] {
		if now.Sub(t) < l.window {
			recent = append(recent, t)
		}
	}
	if len(recent) >= l.max {
		l.starts[key] = recent
		return recent[0].Add(l.window).Sub(now)
	}
	l.starts[key] = append(recent, now)
	// Users who have not exported for a window are forgotten.
	if len(l.starts) > 10_000 {
		for k, ts := range l.starts {
			if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= l.window {
				delete(l.starts, k)
			}
		}
	}
	return 0
}
