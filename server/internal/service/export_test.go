package service

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
)

// exportDoc is the part of the export document the tests look at.
type exportDoc struct {
	Export        bool                        `json:"openbeehive_export"`
	Format        int                         `json:"format"`
	Workspace     string                      `json:"workspace_id"`
	Source        string                      `json:"source"`
	MissingPhotos []string                    `json:"missing_photos"`
	Data          map[string][]map[string]any `json:"data"`
}

func (d exportDoc) ids(entity string) []string {
	var out []string
	for _, r := range d.Data[entity] {
		out = append(out, fmt.Sprint(r["id"]))
	}
	return out
}

func (f *photoFixture) exporter() *Exporter { return NewExporter(f.db, f.blobs) }

// identifyTestCaller trusts the caller the test request names (see caller).
func identifyTestCaller(r *http.Request) (auth.Identity, int) {
	switch {
	case r.Header.Get("X-Test-Unavailable") != "":
		return auth.Identity{}, http.StatusServiceUnavailable
	case r.Header.Get("X-Test-User") == "":
		return auth.Identity{}, http.StatusUnauthorized
	}
	return auth.Identity{
		UserID: r.Header.Get("X-Test-User"), OrgID: r.Header.Get("X-Test-Org"),
		Role: r.Header.Get("X-Test-Role"), ReadOnly: r.Header.Get("X-Test-Read-Only") != "",
	}, 0
}

func setCaller(req *http.Request, who caller) {
	for name, value := range map[string]string{
		"X-Test-User": who.user, "X-Test-Org": who.org, "X-Test-Role": who.role,
	} {
		if value != "" {
			req.Header.Set(name, value)
		}
	}
	for name, on := range map[string]bool{
		"X-Test-Read-Only": who.readOnly, "X-Test-Unavailable": who.unavailable, "X-Test-Cross-Site": who.crossSite,
	} {
		if on {
			req.Header.Set(name, "1")
		}
	}
}

// exportRequest sends one request to the export handler as `who`.
func exportRequest(t *testing.T, e *Exporter, method, path string, who caller) *httptest.ResponseRecorder {
	t.Helper()
	h := e.Handler(identifyTestCaller, func(r *http.Request) bool { return r.Header.Get("X-Test-Cross-Site") == "" })
	req := httptest.NewRequest(method, path, nil)
	setCaller(req, who)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeExport(t *testing.T, body []byte) exportDoc {
	t.Helper()
	var d exportDoc
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatalf("export is not JSON: %v\n%.300s", err, body)
	}
	return d
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestExportHoldsTheWorkspaceWithOwnPersonalTasksOnly(t *testing.T) {
	f := newPhotoFixture(t)
	other := identityCtx("user-a2", "org-a")
	mine, err := f.task.CreateTask(ctxA, &wv1.CreateTaskRequest{Title: "dentist"})
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := f.task.CreateTask(other, &wv1.CreateTaskRequest{Title: "their dentist"})
	if err != nil {
		t.Fatal(err)
	}
	shared, err := f.task.CreateTask(other, &wv1.CreateTaskRequest{Title: "varroa", HiveId: "hive-a"})
	if err != nil {
		t.Fatal(err)
	}
	visit := f.newVisit(t)
	gone, err := f.apiary.CreateApiary(ctxA, &wv1.CreateApiaryRequest{Name: "Gone"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.apiary.DeleteApiary(ctxA, &wv1.DeleteApiaryRequest{Id: gone.Apiary.Id}); err != nil {
		t.Fatal(err)
	}

	rec := exportRequest(t, f.exporter(), http.MethodGet, ExportPath, userA)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, `attachment; filename="openbeehive-server-`) {
		t.Errorf("content disposition %q", cd)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("an export must not be cached: %v", rec.Header())
	}
	d := decodeExport(t, rec.Body.Bytes())
	if !d.Export || d.Format != exportFormat || d.Workspace != "org-a" || d.Source != "server" {
		t.Errorf("document header: %+v", d)
	}
	if got := d.ids("apiary"); len(got) != 1 || got[0] != "apiary-a" {
		t.Errorf("apiaries: %v (other workspaces and deleted ones are left out)", got)
	}
	if got := d.ids("inspection"); !contains(got, visit) {
		t.Errorf("visits: %v", got)
	}
	tasks := d.ids("task")
	if !contains(tasks, mine.Task.Id) || !contains(tasks, shared.Task.Id) || contains(tasks, theirs.Task.Id) {
		t.Errorf("tasks %v: want own personal and shared, not another member's personal task", tasks)
	}
	if _, ok := d.Data["photo"]; ok {
		t.Error("photo records belong only in the ZIP")
	}
	hive := d.Data["hive"][0]
	for _, col := range []string{"deleted", "photo", "field_hlc"} {
		if _, ok := hive[col]; ok {
			t.Errorf("hive carries %q: %v", col, hive)
		}
	}
	if _, ok := d.Data["inspection"][0]["photo_keys"]; ok {
		t.Error("inspection carries the inline photo column")
	}
	if hive["organization_id"] != "org-a" || hive["apiary_id"] != "apiary-a" {
		t.Errorf("hive row: %v", hive)
	}
	for _, entity := range exportEntities {
		if _, ok := d.Data[entity]; !ok {
			t.Errorf("table %s missing (empty tables are listed as [])", entity)
		}
	}
}

func TestExportZipCarriesImagesAndNamesTheMissingOnes(t *testing.T) {
	f := newPhotoFixture(t)
	f.newPhoto(t, "photo-up")
	f.newPhoto(t, "photo-waiting")
	image := jpegBytes(t, 64, 48, 7)
	f.upload(t, "photo-up", image)

	rec := exportRequest(t, f.exporter(), http.MethodGet, ExportPath+"?photos=1", userA)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("export: %d %v", rec.Code, rec.Header())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("not a ZIP: %v", err)
	}
	files := map[string][]byte{}
	for _, file := range zr.File {
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		files[file.Name], _ = io.ReadAll(r)
		_ = r.Close()
	}
	if !bytes.Equal(files["photos/photo-up.jpg"], image) {
		t.Errorf("image of the uploaded photo: %d bytes", len(files["photos/photo-up.jpg"]))
	}
	if _, ok := files["photos/photo-waiting.jpg"]; ok {
		t.Error("a photo without an uploaded image has a file")
	}
	d := decodeExport(t, files[exportDocument])
	if len(d.MissingPhotos) != 1 || d.MissingPhotos[0] != "photo-waiting" {
		t.Errorf("missing photos: %v", d.MissingPhotos)
	}
	photos := d.ids("photo")
	if len(photos) != 2 || !contains(photos, "photo-up") || !contains(photos, "photo-waiting") {
		t.Errorf("photo records: %v", photos)
	}
	for _, p := range d.Data["photo"] {
		if s, _ := p["thumb"].(string); !strings.HasPrefix(s, "data:image/") {
			t.Errorf("photo %v has no thumbnail", p["id"])
		}
		for _, col := range []string{"blob_key", "blob_type"} {
			if _, ok := p[col]; ok {
				t.Errorf("photo record carries %s", col)
			}
		}
	}

	// Another workspace sees none of it.
	rec = exportRequest(t, f.exporter(), http.MethodGet, ExportPath+"?photos=1", userB)
	if bytes.Contains(rec.Body.Bytes(), []byte("photo-up")) {
		t.Error("the export of another workspace names this workspace's photo")
	}
}

func TestExportAuthorization(t *testing.T) {
	f := newPhotoFixture(t)
	e := f.exporter()
	for name, c := range map[string]struct {
		method string
		who    caller
		status int
		code   string
	}{
		"no credentials":      {http.MethodGet, nobody, 401, "not_signed_in"},
		"session unavailable": {http.MethodGet, caller{user: "u", org: "org-a", unavailable: true}, 503, "unavailable"},
		"demo account":        {http.MethodGet, caller{user: "demo", org: "org-a", role: "demo", readOnly: true}, 403, "demo_read_only"},
		"read-only key":       {http.MethodGet, caller{user: "u", org: "org-a", role: "user", readOnly: true}, 403, "read_only"},
		"cross-site":          {http.MethodGet, caller{user: "u", org: "org-a", role: "user", crossSite: true}, 403, "cross_site_request"},
		"POST":                {http.MethodPost, userA, 405, "method_not_allowed"},
	} {
		rec := exportRequest(t, e, c.method, ExportPath, c.who)
		res := transferResult{status: rec.Code, body: rec.Body.Bytes()}
		if res.status != c.status || res.code(t) != c.code {
			t.Errorf("%s: %d %s, want %d %s", name, res.status, res.body, c.status, c.code)
		}
	}
	// Owners and plain members alike.
	for _, who := range []caller{userA, {user: "user-a2", org: "org-a", role: "user"}, {user: "admin", org: "org-a", role: "admin"}} {
		if rec := exportRequest(t, e, http.MethodGet, ExportPath, who); rec.Code != http.StatusOK {
			t.Errorf("%s: %d %s", who.user, rec.Code, rec.Body)
		}
	}
}

func TestExportIsRateLimitedPerUser(t *testing.T) {
	f := newPhotoFixture(t)
	e := f.exporter()
	now := time.Now()
	e.now = func() time.Time { return now }
	for i := range exportsPerHour {
		if rec := exportRequest(t, e, http.MethodGet, ExportPath, userA); rec.Code != http.StatusOK {
			t.Fatalf("export %d: %d", i+1, rec.Code)
		}
	}
	rec := exportRequest(t, e, http.MethodGet, ExportPath, userA)
	res := transferResult{status: rec.Code, body: rec.Body.Bytes()}
	if res.status != http.StatusTooManyRequests || res.code(t) != "rate_limited" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("export over the limit: %d %s %v", res.status, res.body, rec.Header())
	}
	if rec := exportRequest(t, e, http.MethodGet, ExportPath, userB); rec.Code != http.StatusOK {
		t.Errorf("another user has an allowance of their own: %d", rec.Code)
	}
	// Refused requests do not count; an hour after the first, one more is allowed.
	now = now.Add(time.Hour + time.Second)
	if rec := exportRequest(t, e, http.MethodGet, ExportPath, userA); rec.Code != http.StatusOK {
		t.Errorf("after an hour: %d", rec.Code)
	}
}

// countingWriter records how the response arrives.
type countingWriter struct {
	*httptest.ResponseRecorder
	writes int
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.writes++
	return c.ResponseRecorder.Write(p)
}

func TestExportStreamsALargeWorkspaceInPages(t *testing.T) {
	f := newPhotoFixture(t)
	const visits = 2*exportPage + 37
	for start := 0; start < visits; start += 250 {
		var values []string
		var args []any
		for i := start; i < start+250 && i < visits; i++ {
			values = append(values, "(?, 'org-a', 'hive-a', '2026-05-01', '2026-05-01', ?, FALSE)")
			args = append(args, fmt.Sprintf("visit-%05d", i), strings.Repeat("n", 200))
		}
		q := `INSERT INTO inspection (id, organization_id, hive_id, date, created_at, note, deleted) VALUES ` + strings.Join(values, ", ")
		if _, err := f.db.Exec(f.db.Rebind(q), args...); err != nil {
			t.Fatalf("seed visits: %v", err)
		}
	}
	// One more workspace's visit sorts between them and must not appear.
	if _, err := f.db.Exec(`INSERT INTO inspection (id, organization_id, hive_id, date, created_at, deleted) VALUES ('visit-00100x', 'org-b', 'hive-b', '2026-05-01', '2026-05-01', FALSE)`); err != nil {
		t.Fatal(err)
	}

	h := f.exporter().Handler(identifyTestCaller, nil)
	req := httptest.NewRequest(http.MethodGet, ExportPath, nil)
	setCaller(req, userA)
	w := &countingWriter{ResponseRecorder: httptest.NewRecorder()}
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("export: %d", w.Code)
	}
	d := decodeExport(t, w.Body.Bytes())
	got := d.ids("inspection")
	if len(got) != visits {
		t.Fatalf("visits exported: %d, want %d", len(got), visits)
	}
	for i, id := range got {
		if id != fmt.Sprintf("visit-%05d", i) {
			t.Fatalf("visit %d is %s: pages skipped or repeated a row", i, id)
		}
	}
	// The document goes out as it is read, in buffer-sized pieces, instead
	// of being assembled in memory first.
	if min := w.Body.Len() / (64 << 10); w.writes < min {
		t.Errorf("%d bytes arrived in %d writes; want at least %d", w.Body.Len(), w.writes, min)
	}
}
