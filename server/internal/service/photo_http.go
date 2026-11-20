package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage/blob"
)

// Transfer of full-size images, under PhotoPathPrefix:
//
//	GET  /photos/<photo id>   the image, for members of the photo's workspace
//	POST /photos/<photo id>   upload it; the body is the image itself
//
// Both need the caller's session or API key. The photo record must already
// exist (it syncs first); it decides the workspace, and the blob key is
// derived from it, never from the request. An upload is accepted once per
// photo: repeating it (a device that never saw the answer) is acknowledged
// without replacing what is stored, so the image behind an id never changes
// and may be cached for good.

// PhotoIdentify authenticates a transfer request. status is 0 on success,
// otherwise the HTTP status to answer with.
type PhotoIdentify func(r *http.Request) (id auth.Identity, status int)

// Handler serves the transfer endpoints. sameSite is the cross-site rule for
// state-changing requests (uploads); nil allows all, for setups that accept
// no cookies.
func (p *Photos) Handler(identify PhotoIdentify, sameSite func(*http.Request) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		photoID := strings.TrimPrefix(r.URL.Path, PhotoPathPrefix)
		if !addressable(photoID) {
			photoError(w, http.StatusNotFound, "not_found", "no such photo")
			return
		}
		upload := r.Method == http.MethodPost
		if !upload && r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD, POST")
			photoError(w, http.StatusMethodNotAllowed, "method_not_allowed", "this endpoint requires GET or POST")
			return
		}
		if upload && sameSite != nil && !sameSite(r) {
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
		if upload {
			p.upload(w, r, id, photoID)
			return
		}
		p.download(w, r, id, photoID)
	})
}

// addressable reports whether a path segment can be a photo id: one
// segment of printable text. Anything else names no photo, and is not even
// looked up.
func addressable(photoID string) bool {
	if photoID == "" || len(photoID) > maxIDLen || !utf8.ValidString(photoID) {
		return false
	}
	return !strings.ContainsFunc(photoID, func(r rune) bool { return r == '/' || unicode.IsControl(r) })
}

func photoError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message, "code": code})
}

func photoInternal(w http.ResponseWriter, what string, err error) {
	log.Printf("photos: %s: %v", what, err)
	photoError(w, http.StatusInternalServerError, "internal", "something went wrong on the server")
}

// download streams the full-size image. A photo of another workspace is
// answered exactly like one that does not exist.
func (p *Photos) download(w http.ResponseWriter, r *http.Request, id auth.Identity, photoID string) {
	row, err := photoByID(r.Context(), p.db, id.OrgID, photoID)
	if err != nil {
		photoInternal(w, "look up photo", err)
		return
	}
	if row == nil || row.Deleted {
		photoError(w, http.StatusNotFound, "not_found", "no such photo")
		return
	}
	if row.BlobKey == "" || p.blobs == nil {
		photoError(w, http.StatusNotFound, "not_uploaded", "the full-size image has not been uploaded yet")
		return
	}
	body, size, err := p.blobs.Get(r.Context(), row.BlobKey)
	if errors.Is(err, blob.ErrNotFound) {
		photoError(w, http.StatusNotFound, "not_uploaded", "the full-size image is not available")
		return
	}
	if err != nil {
		photoInternal(w, "read blob", err)
		return
	}
	defer func() { _ = body.Close() }()

	h := w.Header()
	// The type is the one found in the bytes at upload, and browsers are told
	// not to guess another (nosniff comes with every response).
	h.Set("Content-Type", row.BlobType)
	h.Set("Content-Length", strconv.FormatInt(size, 10))
	h.Set("Content-Disposition", `inline; filename="photo`+extensionOf(row.BlobType)+`"`)
	// Private: only this user's browser may keep it, never a shared cache.
	// The image behind an id never changes.
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	h.Set("Vary", "Authorization, Cookie")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	// If a browser is ever made to open the response as a document, nothing in it runs.
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, body)
}

func extensionOf(contentType string) string {
	switch contentType {
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	}
	return ".jpg"
}

// upload stores the full-size image of a photo whose record exists.
func (p *Photos) upload(w http.ResponseWriter, r *http.Request, id auth.Identity, photoID string) {
	switch {
	case id.Role == "demo":
		photoError(w, http.StatusForbidden, "demo_read_only", auth.ErrDemoReadOnly.Error())
		return
	case id.ReadOnly:
		photoError(w, http.StatusForbidden, "read_only", auth.ErrKeyReadOnly.Error())
		return
	case p.blobs == nil:
		photoError(w, http.StatusServiceUnavailable, "unavailable", "photo storage is not configured")
		return
	}
	row, err := photoByID(r.Context(), p.db, id.OrgID, photoID)
	switch {
	case err != nil:
		photoInternal(w, "look up photo", err)
		return
	case row == nil:
		// Also the answer for a photo of another workspace. A device whose
		// record has not been pushed yet simply tries again after its sync.
		photoError(w, http.StatusNotFound, "unknown_photo", "no such photo; its record must be synced first")
		return
	case row.Deleted:
		photoError(w, http.StatusGone, "deleted", "this photo has been deleted")
		return
	case row.BlobKey != "":
		photoStored(w, http.StatusOK, row.BlobType, row.BlobSize)
		return
	}
	data, ok := readPhotoBody(w, r)
	if !ok {
		return
	}
	typ := sniffPhotoType(data)
	if typ == "" {
		photoError(w, http.StatusUnsupportedMediaType, "unsupported_type", "the image must be a JPEG, PNG or WebP file")
		return
	}
	p.store(w, r, row, data, typ)
}

// readPhotoBody reads the request body up to the size cap. On failure it
// answers and returns false.
func readPhotoBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.ContentLength > MaxPhotoBytes {
		photoError(w, http.StatusRequestEntityTooLarge, "too_large", "the image exceeds 5 MiB")
		return nil, false
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxPhotoBytes))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		photoError(w, http.StatusRequestEntityTooLarge, "too_large", "the image exceeds 5 MiB")
		return nil, false
	case err != nil:
		photoError(w, http.StatusBadRequest, "invalid_body", "the upload was interrupted")
		return nil, false
	case len(data) == 0:
		photoError(w, http.StatusBadRequest, "invalid_body", "the request body must be the image")
		return nil, false
	}
	return data, true
}

// store writes the blob, then records it on the row. The row is the
// authority: when it was deleted or filled in the meantime, the blob just
// written is not the photo's and goes away again.
func (p *Photos) store(w http.ResponseWriter, r *http.Request, row *photoRow, data []byte, typ string) {
	ctx := r.Context()
	key := blob.PhotoKey(row.OrgID, row.ID)
	if err := p.blobs.Put(ctx, key, bytes.NewReader(data), int64(len(data)), typ); err != nil {
		photoInternal(w, "store blob", err)
		return
	}
	res, err := p.db.ExecContext(ctx, p.db.Rebind(`UPDATE photo SET blob_key = ?, blob_type = ?, blob_size = ?
		WHERE id = ? AND organization_id = ? AND deleted = FALSE AND blob_key = ''`),
		key, typ, len(data), row.ID, row.OrgID)
	if err != nil {
		photoInternal(w, "record blob", err) // the sweep removes the blob as one without a record
		return
	}
	if n, _ := res.RowsAffected(); n == 1 {
		photoStored(w, http.StatusCreated, typ, int64(len(data)))
		return
	}
	cur, err := photoByID(ctx, p.db, row.OrgID, row.ID)
	if err != nil {
		photoInternal(w, "look up photo", err)
		return
	}
	if cur != nil && !cur.Deleted && cur.BlobKey == key {
		// A second upload of the same photo got there first.
		photoStored(w, http.StatusOK, cur.BlobType, cur.BlobSize)
		return
	}
	if err := p.blobs.Delete(ctx, key); err != nil {
		log.Printf("photos: blob of deleted photo not removed (the sweep retries): %v", err)
	}
	photoError(w, http.StatusGone, "deleted", "this photo has been deleted")
}

func photoStored(w http.ResponseWriter, status int, contentType string, size int64) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "stored", "content_type": contentType, "size_bytes": size})
}
