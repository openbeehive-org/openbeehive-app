package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
)

func newTestRouter(cfg *config.Config) (*Router, *int) {
	calls := 0
	ok := func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}
	rt := NewRouter(cfg)
	rt.Get("/auth/read", ok)
	rt.Post("/auth/act", ok)
	rt.PostJSON("/auth/change", func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		if !decodeJSON(w, r, &v) {
			return
		}
		ok(w, r)
	})
	rt.Get("/auth/both", ok)
	rt.PostJSON("/auth/both", ok)
	return rt, &calls
}

func serve(rt *Router, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://bees.example.org"+path, strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	return rec
}

func TestRouterEnforcesMethods(t *testing.T) {
	rt, calls := newTestRouter(&config.Config{})
	for _, c := range []struct {
		method, path string
		want         int
		allow        string
	}{
		{http.MethodGet, "/auth/read", http.StatusNoContent, ""},
		{http.MethodPost, "/auth/read", http.StatusMethodNotAllowed, "GET"},
		{http.MethodGet, "/auth/act", http.StatusMethodNotAllowed, "POST"},
		{http.MethodGet, "/auth/change", http.StatusMethodNotAllowed, "POST"},
		{http.MethodDelete, "/auth/both", http.StatusMethodNotAllowed, "GET, POST"},
		{http.MethodGet, "/auth/nope", http.StatusNotFound, ""},
		{http.MethodGet, "/auth/read/", http.StatusNotFound, ""},
	} {
		rec := serve(rt, c.method, c.path, "")
		if rec.Code != c.want || rec.Header().Get("Allow") != c.allow {
			t.Errorf("%s %s: got %d (Allow %q), want %d (Allow %q)", c.method, c.path, rec.Code, rec.Header().Get("Allow"), c.want, c.allow)
		}
		if c.want >= 400 && !strings.Contains(rec.Body.String(), `"code":`) {
			t.Errorf("%s %s: error is not the JSON contract: %s", c.method, c.path, rec.Body)
		}
	}
	if *calls != 1 {
		t.Fatalf("handlers ran %d times, want only the one allowed request", *calls)
	}
}

func TestRouterRejectsCrossSiteStateChanges(t *testing.T) {
	cfg := &config.Config{PublicBaseURL: "https://public.example.org"}
	cfg.CORS.AllowedOrigins = []string{"https://app.example.org"}
	rt, calls := newTestRouter(cfg)
	json := []string{"Content-Type", "application/json"}

	for _, c := range []struct {
		name    string
		headers []string
		want    int
	}{
		{"script or CLI without browser headers", nil, http.StatusNoContent},
		{"same origin", []string{"Origin", "https://bees.example.org", "Sec-Fetch-Site", "same-origin"}, http.StatusNoContent},
		{"same host, proxy terminated TLS", []string{"Origin", "http://bees.example.org"}, http.StatusNoContent},
		{"public base URL", []string{"Origin", "https://public.example.org"}, http.StatusNoContent},
		{"configured CORS origin", []string{"Origin", "https://APP.example.org", "Sec-Fetch-Site", "same-site"}, http.StatusNoContent},
		{"foreign origin", []string{"Origin", "https://evil.example", "Sec-Fetch-Site", "cross-site"}, http.StatusForbidden},
		{"sibling subdomain not configured", []string{"Origin", "https://other.example.org"}, http.StatusForbidden},
		{"opaque origin", []string{"Origin", "null"}, http.StatusForbidden},
		{"cross-site without Origin", []string{"Sec-Fetch-Site", "cross-site"}, http.StatusForbidden},
		{"same-site without Origin", []string{"Sec-Fetch-Site", "same-site"}, http.StatusForbidden},
	} {
		before := *calls
		rec := serve(rt, http.MethodPost, "/auth/change", `{}`, append(json, c.headers...)...)
		if rec.Code != c.want {
			t.Errorf("%s: got %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body)
		}
		if c.want == http.StatusForbidden {
			if !strings.Contains(rec.Body.String(), `"code":"cross_site_request"`) {
				t.Errorf("%s: wrong error: %s", c.name, rec.Body)
			}
			if *calls != before {
				t.Errorf("%s: the handler ran despite the rejection", c.name)
			}
		}
		// Body-less state changes (logout) are covered by the same check.
		if rec := serve(rt, http.MethodPost, "/auth/act", "", c.headers...); rec.Code != c.want {
			t.Errorf("%s (no body): got %d, want %d", c.name, rec.Code, c.want)
		}
	}

	// Reads stay reachable from anywhere: links in emails, the OIDC redirect.
	if rec := serve(rt, http.MethodGet, "/auth/read", "", "Sec-Fetch-Site", "cross-site"); rec.Code != http.StatusNoContent {
		t.Errorf("cross-site GET of a read route: %d", rec.Code)
	}

	// An operator who allows any origin gets exactly that.
	open := &config.Config{}
	open.CORS.AllowedOrigins = []string{"*"}
	rtOpen, _ := newTestRouter(open)
	if rec := serve(rtOpen, http.MethodPost, "/auth/change", `{}`, append(json, "Origin", "https://anything.example")...); rec.Code != http.StatusNoContent {
		t.Errorf("wildcard origin: %d", rec.Code)
	}
}

func TestRouterRequiresJSONAndCapsBodies(t *testing.T) {
	rt, calls := newTestRouter(&config.Config{})

	// What an HTML form can send across sites is not accepted as a body.
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x"} {
		rec := serve(rt, http.MethodPost, "/auth/change", `{}`, "Content-Type", ct)
		if rec.Code != http.StatusUnsupportedMediaType || !strings.Contains(rec.Body.String(), `"code":"unsupported_media_type"`) {
			t.Errorf("content type %q: got %d %s", ct, rec.Code, rec.Body)
		}
	}
	if rec := serve(rt, http.MethodPost, "/auth/change", `{}`, "Content-Type", "application/json; charset=utf-8"); rec.Code != http.StatusNoContent {
		t.Errorf("json with charset: %d", rec.Code)
	}
	for _, body := range []string{"", "not json", `[1`} {
		rec := serve(rt, http.MethodPost, "/auth/change", body, "Content-Type", "application/json")
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"invalid_body"`) {
			t.Errorf("body %q: got %d %s", body, rec.Code, rec.Body)
		}
	}
	big := `{"pad":"` + strings.Repeat("x", MaxJSONBody) + `"}`
	rec := serve(rt, http.MethodPost, "/auth/change", big, "Content-Type", "application/json")
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), `"code":"body_too_large"`) {
		t.Errorf("oversized body: got %d %s", rec.Code, rec.Body)
	}
	if *calls != 1 {
		t.Fatalf("handlers ran %d times, want 1", *calls)
	}
}

func TestClientIP(t *testing.T) {
	for _, c := range []struct {
		name, remote, xff string
		trust             bool
		want              string
	}{
		{"direct", "203.0.113.7:4711", "", false, "203.0.113.7"},
		{"forged header is ignored without a trusted proxy", "203.0.113.7:4711", "198.51.100.1", false, "203.0.113.7"},
		{"proxy appended the client", "10.0.0.2:80", "198.51.100.1", true, "198.51.100.1"},
		{"client-supplied entries before the proxy's are ignored", "10.0.0.2:80", "1.2.3.4, 198.51.100.9", true, "198.51.100.9"},
		{"trusted proxy but no header", "10.0.0.2:80", "", true, "10.0.0.2"},
		{"IPv6 counts per /64", "[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:443", "", false, "2001:db8:1:2::"},
		{"IPv4-mapped", "[::ffff:203.0.113.7]:443", "", false, "203.0.113.7"},
	} {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := ClientIP(r, c.trust); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
