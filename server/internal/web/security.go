package web

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
)

// SecurityHeaders adds the response headers every route shares: no MIME
// sniffing, no referrer (invite and verification links carry tokens in their
// query), HSTS when the public URL is https, and the Content-Security-Policy.
// indexHTML is the app shell whose inline scripts the policy must allow; nil
// when the server does not serve the app.
func SecurityHeaders(cfg *config.Config, indexHTML []byte) func(http.Handler) http.Handler {
	csp := ContentSecurityPolicy(cfg, indexHTML)
	https := strings.HasPrefix(strings.ToLower(cfg.PublicBaseURL), "https://")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Content-Security-Policy", csp)
			if https {
				h.Set("Strict-Transport-Security", "max-age=31536000")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ContentSecurityPolicy builds the policy the app runs under: everything
// from the server's own origin, plus exactly what the app needs beyond that:
//
//   - the inline bootstrap script(s) of index.html, allowed by hash,
//   - WebAssembly (the local SQLite database) and its worker,
//   - data: and blob: images (photos and previews),
//   - the configured map tile and geocoder origins, when maps are on.
//
// Nothing may frame the app.
func ContentSecurityPolicy(cfg *config.Config, indexHTML []byte) string {
	script := []string{"'self'", "'wasm-unsafe-eval'"}
	for _, hash := range InlineScriptHashes(indexHTML) {
		script = append(script, "'"+hash+"'")
	}
	img := []string{"'self'", "data:", "blob:"}
	connect := []string{"'self'"}
	if cfg.Maps.Enabled {
		img = append(img, cfg.Maps.TileOrigin)
		// The service worker fetches tiles on the page's behalf, which
		// counts as a connection, not an image load.
		connect = append(connect, cfg.Maps.GeocoderOrigin, cfg.Maps.TileOrigin)
	}
	return strings.Join([]string{
		"default-src 'self'",
		"script-src " + strings.Join(script, " "),
		// Svelte and Leaflet position elements through style attributes.
		"style-src 'self' 'unsafe-inline'",
		"img-src " + strings.Join(img, " "),
		"font-src 'self'",
		"connect-src " + strings.Join(connect, " "),
		"worker-src 'self' blob:",
		"manifest-src 'self'",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")
}

var scriptTag = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`)

// InlineScriptHashes returns the CSP hash source ("sha256-...") of every
// inline script in the document, in order. Scripts loaded with src= are
// covered by 'self' and skipped.
func InlineScriptHashes(html []byte) []string {
	var out []string
	for _, m := range scriptTag.FindAllSubmatch(html, -1) {
		if strings.Contains(strings.ToLower(string(m[1])), "src=") {
			continue
		}
		sum := sha256.Sum256(m[2])
		out = append(out, "sha256-"+base64.StdEncoding.EncodeToString(sum[:]))
	}
	return out
}

// HostGuard protects a server that runs without login. There, every request
// acts as the local user, so a web page the user merely visits must not be
// able to reach the server under a name of its own choosing (DNS rebinding).
// Requests pass only when their Host is localhost, an IP address, the host of
// BEEHIVE_PUBLIC_BASE_URL, or listed in BEEHIVE_ALLOWED_HOSTS; "*" in that
// list turns the check off.
func HostGuard(cfg *config.Config) func(http.Handler) http.Handler {
	allowed := map[string]bool{cfg.PublicHost(): true}
	for _, h := range cfg.AllowedHosts {
		if h == "*" {
			return func(next http.Handler) http.Handler { return next }
		}
		allowed[hostName(h)] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := hostName(r.Host)
			if host != "" && (allowed[host] || isLocalName(host)) {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			//nolint:gosec // G705: plain text with nosniff, and the name is quoted
			_, _ = fmt.Fprintf(w, "This Openbeehive server runs without login and does not answer to the host name %q.\n"+
				"Add it to BEEHIVE_ALLOWED_HOSTS (comma-separated; \"*\" allows any name), "+
				"or set BEEHIVE_PUBLIC_BASE_URL to the address you use.\n", host)
		})
	}
}

// hostName reduces a Host header or configured entry to a bare lower-case
// name: no port, no IPv6 brackets, no trailing dot.
func hostName(hostport string) string {
	host := strings.TrimSpace(hostport)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// isLocalName reports whether a host is localhost or an IP address literal.
// A page on another site can make the browser resolve a foreign name to this
// machine, but it cannot share an origin with an address typed as such.
func isLocalName(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	_, err := netip.ParseAddr(host)
	return err == nil
}

// NoDeadline lifts the server's read and write timeouts for the listed
// request paths. Streaming RPCs stay open for as long as the client listens;
// every other route keeps the timeouts.
func NoDeadline(paths map[string]bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if paths[r.URL.Path] {
				rc := http.NewResponseController(w)
				_ = rc.SetReadDeadline(time.Time{})
				_ = rc.SetWriteDeadline(time.Time{})
			}
			next.ServeHTTP(w, r)
		})
	}
}
