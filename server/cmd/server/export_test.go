package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
)

// The workspace export behind the real authentication: sessions and API
// keys, the cross-site rule and the CORS setup.
func TestExportWithRealCredentials(t *testing.T) {
	cfg := loginConfig()
	cfg.CORS.AllowedOrigins = []string{"https://app.example.org"}
	cfg.CORS.AllowCredentials = true
	w := newWired(t, cfg)
	owner, stranger := w.signup("owner@example.org"), w.signup("stranger@example.org")
	writeKey, readKey := w.apiKey(owner, "write"), w.apiKey(owner, "read")
	w.newPhoto(owner, "photo-1")
	auth := func(token string) []string { return []string{"Authorization", "Bearer " + token} }
	cookie := "obh_session=" + owner.token

	workspaceOf := func(res reply) string {
		var doc struct {
			Workspace string                      `json:"workspace_id"`
			Data      map[string][]map[string]any `json:"data"`
		}
		if err := json.Unmarshal(res.body, &doc); err != nil {
			t.Fatalf("not an export: %q", res.body)
		}
		return doc.Workspace
	}

	for name, headers := range map[string][]string{
		"session":                auth(owner.token),
		"write key":              auth(writeKey),
		"cookie, same origin":    {"Cookie", cookie, "Sec-Fetch-Site", "same-origin"},
		"cookie, allowed origin": {"Cookie", cookie, "Origin", "https://app.example.org"},
	} {
		res := w.do(http.MethodGet, "/export", nil, headers...)
		if res.status != http.StatusOK || workspaceOf(res) != owner.org {
			t.Errorf("export with %s: %d %.200s", name, res.status, res.body)
		}
	}
	// A member of another workspace gets their own, never this one.
	if res := w.do(http.MethodGet, "/export", nil, auth(stranger.token)...); res.status != http.StatusOK ||
		workspaceOf(res) != stranger.org || strings.Contains(string(res.body), "Home") {
		t.Errorf("export of another member: %d %.200s", res.status, res.body)
	}

	for name, c := range map[string]struct {
		headers []string
		status  int
		code    string
	}{
		"no credentials":       {nil, 401, "not_signed_in"},
		"read-only key":        {auth(readKey), 403, "read_only"},
		"cookie, cross-site":   {[]string{"Cookie", cookie, "Sec-Fetch-Site", "cross-site"}, 403, "cross_site_request"},
		"cookie, other origin": {[]string{"Cookie", cookie, "Origin", "https://evil.example"}, 403, "cross_site_request"},
	} {
		res := w.do(http.MethodGet, "/export", nil, c.headers...)
		if res.status != c.status || res.json(t)["code"] != c.code {
			t.Errorf("export with %s: %d %s, want %d %s", name, res.status, res.body, c.status, c.code)
		}
		// Whatever the answer, no other site is allowed to read it.
		if acao := res.header.Get("Access-Control-Allow-Origin"); acao != "" {
			t.Errorf("export with %s: Access-Control-Allow-Origin %q", name, acao)
		}
	}

	// The ZIP comes through the same door.
	res := w.do(http.MethodGet, "/export?photos=1", nil, auth(owner.token)...)
	if res.status != http.StatusOK || res.header.Get("Content-Type") != "application/zip" ||
		!strings.HasPrefix(string(res.body), "PK") {
		t.Errorf("ZIP export: %d %v", res.status, res.header)
	}
	if res.header.Get("Cross-Origin-Resource-Policy") != "same-origin" {
		t.Errorf("ZIP export headers: %v", res.header)
	}
}

// Without login the endpoint acts as the local user and still refuses what
// another site makes the browser send.
func TestExportWithoutLogin(t *testing.T) {
	w := newWired(t, &config.Config{Profile: "selfhost", PublicBaseURL: "http://localhost:8080"})
	if res := w.do(http.MethodGet, "/export", nil); res.status != http.StatusOK {
		t.Errorf("export: %d %s", res.status, res.body)
	}
	if res := w.do(http.MethodGet, "/export", nil, "Sec-Fetch-Site", "cross-site"); res.status != http.StatusForbidden {
		t.Errorf("cross-site export: %d", res.status)
	}
}
