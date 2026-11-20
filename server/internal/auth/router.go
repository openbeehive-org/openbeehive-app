package auth

import (
	"encoding/json"
	"errors"
	"log"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
)

// MaxJSONBody caps the request body of every plain-HTTP auth and workspace
// endpoint. They carry credentials, ids and passkey ceremonies, never data.
const MaxJSONBody = 64 << 10

// Router serves the plain-HTTP endpoints under /auth/ and /tenants/ and
// applies the same rules to all of them:
//
//   - every route declares its method; anything else is 405,
//   - state-changing routes are POST and refuse cross-site browser requests,
//     so a session cookie cannot be abused by a link or form on another site,
//   - routes that read a body require Content-Type: application/json, which a
//     cross-site form cannot send without a CORS preflight,
//   - request bodies are capped at MaxJSONBody,
//   - errors are JSON {"error", "code"}.
//
// Routes are registered once at startup; the Router is read-only afterwards.
type Router struct {
	routes    map[string]map[string]route // path -> method -> route
	origins   map[string]bool             // origins allowed besides the request's own
	anyOrigin bool
}

type route struct {
	handler  http.HandlerFunc
	jsonBody bool
}

// NewRouter builds a router that accepts state-changing requests from the
// server's own origin, the public base URL and the configured CORS origins.
func NewRouter(cfg *config.Config) *Router {
	rt := &Router{routes: map[string]map[string]route{}, origins: map[string]bool{}}
	for _, o := range append([]string{cfg.PublicBaseURL}, cfg.CORS.AllowedOrigins...) {
		if o == "*" {
			rt.anyOrigin = true
			continue
		}
		if u, err := url.Parse(o); err == nil && u.Host != "" {
			rt.origins[strings.ToLower(u.Scheme+"://"+u.Host)] = true
		}
	}
	return rt
}

// Get registers a read-only route.
func (rt *Router) Get(path string, h http.HandlerFunc) {
	rt.add(http.MethodGet, path, route{handler: h})
}

// Post registers a state-changing route that needs no JSON body (logout,
// passkey ceremonies whose body the WebAuthn library parses).
func (rt *Router) Post(path string, h http.HandlerFunc) {
	rt.add(http.MethodPost, path, route{handler: h})
}

// PostJSON registers a state-changing route that reads a JSON body.
func (rt *Router) PostJSON(path string, h http.HandlerFunc) {
	rt.add(http.MethodPost, path, route{handler: h, jsonBody: true})
}

func (rt *Router) add(method, path string, r route) {
	if rt.routes[path] == nil {
		rt.routes[path] = map[string]route{}
	}
	rt.routes[path][method] = r
}

func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	methods, ok := rt.routes[r.URL.Path]
	if !ok {
		respondError(w, http.StatusNotFound, "not_found", "unknown endpoint")
		return
	}
	rte, ok := methods[r.Method]
	if !ok {
		allowed := make([]string, 0, len(methods))
		for m := range methods {
			allowed = append(allowed, m)
		}
		sort.Strings(allowed)
		w.Header().Set("Allow", strings.Join(allowed, ", "))
		respondError(w, http.StatusMethodNotAllowed, "method_not_allowed", "this endpoint requires "+strings.Join(allowed, " or "))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxJSONBody)
	if r.Method == http.MethodPost {
		if !rt.sameSite(r) {
			respondError(w, http.StatusForbidden, "cross_site_request", "cross-site requests are not allowed; if this is the app's own address, set BEEHIVE_PUBLIC_BASE_URL to it")
			return
		}
		if rte.jsonBody && !isJSON(r) {
			respondError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
			return
		}
	}
	rte.handler(w, r)
}

// sameSite reports whether a request may change state: it comes from the
// server's own origin, from an allowed origin, or from a non-browser client
// (which sends neither Origin nor Sec-Fetch-Site and carries no ambient
// cookie an attacker could ride on).
func (rt *Router) sameSite(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		if rt.anyOrigin {
			return true
		}
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" {
			return false // includes the opaque origin "null"
		}
		return strings.EqualFold(u.Host, r.Host) || rt.origins[strings.ToLower(u.Scheme+"://"+u.Host)]
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return true
	}
	return false
}

func isJSON(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "application/json"
}

func respondJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// respondError writes the error shape every auth and workspace endpoint
// uses: a plain-English message plus a stable snake_case code that clients
// map to a translation.
func respondError(w http.ResponseWriter, status int, code, message string) {
	respondJSON(w, status, map[string]any{"error": message, "code": code})
}

// internalError logs the cause and answers with a generic 500: database and
// driver messages never reach the client.
func internalError(w http.ResponseWriter, what string, err error) {
	log.Printf("auth: %s: %v", what, err)
	respondError(w, http.StatusInternalServerError, "internal", "something went wrong on the server")
}

// redirectError answers a request the browser opened as a page (an emailed
// link, the return from a sign-in provider): it goes on to the sign-in page,
// which shows the translated message for the code. JSON would leave the user
// looking at raw text.
func redirectError(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/login?error="+url.QueryEscape(code), http.StatusFound)
}

// redirectInternalError logs the cause and sends the browser to the sign-in
// page with the generic server-error code.
func redirectInternalError(w http.ResponseWriter, r *http.Request, what string, err error) {
	log.Printf("auth: %s: %v", what, err)
	redirectError(w, r, "internal")
}

func notSignedIn(w http.ResponseWriter) {
	respondError(w, http.StatusUnauthorized, "not_signed_in", "not signed in")
}

func demoReadOnly(w http.ResponseWriter) {
	respondError(w, http.StatusForbidden, "demo_read_only", ErrDemoReadOnly.Error())
}

// decodeJSON reads the request body into v. On failure it answers 413 (body
// over MaxJSONBody) or 400 and returns false.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	err := json.NewDecoder(r.Body).Decode(v)
	if err == nil {
		return true
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		respondError(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body too large")
		return false
	}
	respondError(w, http.StatusBadRequest, "invalid_body", "the request body must be a JSON object")
	return false
}

// ClientIP returns the address throttling counts against. Behind a reverse
// proxy (trustProxy) that is the last X-Forwarded-For entry, the one the
// proxy itself appended; a client cannot forge it. With a CDN in front of
// that proxy, the last entry is the CDN's address. IPv6 addresses are reduced
// to their /64, the block one subscriber typically controls.
func ClientIP(r *http.Request, trustProxy bool) string {
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		ip = host
	}
	if !trustProxy && r.Header.Get("X-Forwarded-For") != "" {
		untrustedProxyOnce.Do(func() { log.Print(untrustedProxyWarning) })
	}
	if trustProxy {
		parts := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
		if last := strings.TrimSpace(parts[len(parts)-1]); last != "" {
			ip = last
		}
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.Unmap()
	if addr.Is6() {
		if p, err := addr.Prefix(64); err == nil {
			return p.Addr().String()
		}
	}
	return addr.String()
}

// untrustedProxyOnce logs untrustedProxyWarning once per process.
var untrustedProxyOnce sync.Once

const untrustedProxyWarning = "WARNING: requests carry X-Forwarded-For, but BEEHIVE_TRUST_PROXY_HEADERS is false: " +
	"sign-in throttling counts every client as the proxy's address, so a few wrong passwords lock everyone out. " +
	"Set BEEHIVE_TRUST_PROXY_HEADERS=true when a reverse proxy you run sets the header."
