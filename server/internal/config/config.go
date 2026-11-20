// Package config reads the runtime configuration from environment variables.
//
// Core idea: a single binary serves both worlds:
//   - cloud:    PostgreSQL + MinIO   (central public service)
//   - selfhost: SQLite   + filesystem (single beekeeper, single server)
//
// BEEHIVE_DEPLOYMENT_PROFILE sets defaults; every option can be overridden. There are
// no hard-coded operational values elsewhere in the server; everything the
// process needs at runtime is read here, once, into this struct at bootstrap.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type DBDriver string

const (
	DriverPostgres DBDriver = "postgres"
	DriverMySQL    DBDriver = "mysql"
	DriverSQLite   DBDriver = "sqlite"
)

type BlobBackend string

const (
	BlobMinIO BlobBackend = "minio"
	BlobFS    BlobBackend = "fs"
)

type Config struct {
	Profile       string // cloud | selfhost
	Addr          string
	PublicBaseURL string
	// EnvFile is true when a .env file in the working directory was applied.
	EnvFile bool
	// Warnings are non-fatal remarks about the configuration, logged at boot.
	Warnings []string

	// TrustProxyHeaders takes the client IP for sign-in throttling from the
	// last X-Forwarded-For entry. Only for deployments behind exactly one
	// reverse proxy that appends it.
	TrustProxyHeaders bool
	// AllowedHosts are extra Host names accepted when no login is configured
	// (see web.HostGuard); "*" disables the check.
	AllowedHosts []string
	Maps         MapsConfig

	Server ServerConfig
	CORS   CORSConfig
	Sync   SyncConfig
	Web    WebConfig

	DB struct {
		Driver DBDriver
		DSN    string
	}

	Blob struct {
		Backend BlobBackend
		// MinIO / S3
		Endpoint  string
		AccessKey string
		SecretKey string
		Bucket    string
		UseSSL    bool
		// filesystem
		BaseDir   string
		PublicURL string // base URL under which FS blobs are served
	}

	Auth AuthConfig

	OIDC struct {
		// comma-separated list of active providers, e.g. "google,entra,keycloak"
		Enabled     []string
		RedirectURL string
		Providers   map[string]OIDCProvider
	}

	Demo DemoConfig
}

// DemoConfig installs a read-only demo tenant (seed data, reset hourly) for
// showcasing. Off by default; enable with BEEHIVE_DEMO=true. It coexists with
// normal registration/invites; AutoLogin additionally signs anonymous
// visitors straight into the demo (pure-showcase hosts only).
type DemoConfig struct {
	Enabled   bool
	AutoLogin bool
	Email     string
	Password  string
}

// MapsConfig controls the only remote resources the app loads: map tiles and
// address search. Disabled, the app shows no map and looks nothing up.
type MapsConfig struct {
	Enabled     bool
	TileURL     string // Leaflet template, e.g. https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png
	GeocoderURL string // Nominatim-compatible base URL
	// Origins derived from the URLs above for the Content-Security-Policy;
	// empty when maps are disabled.
	TileOrigin     string
	GeocoderOrigin string
}

type ServerConfig struct {
	ReadHeaderTimeout time.Duration
	// ReadTimeout and WriteTimeout bound every request except streaming RPCs
	// (Subscribe), which stay open; 0 = no limit.
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
}

// CORSConfig lists the other origins that may call the API from a browser.
// Empty means same-origin only. With "*" credentials are never allowed.
type CORSConfig struct {
	AllowedOrigins   []string
	AllowCredentials bool
}

type SyncConfig struct {
	NodeID string // HLC node id stamped on server-side changes
	// MinProtocol is the lowest sync protocol version accepted from apps.
	// 0 admits the 0.4.x apps, which announce none.
	MinProtocol int
}

type WebConfig struct {
	Serve bool   // serve the embedded SPA (single-binary production)
	Dir   string // optional external dir override; empty = embedded build
}

type AuthConfig struct {
	SessionSecret string        // HMAC secret for app-session JWTs
	SessionTTL    time.Duration // app-session lifetime
	WebAuthn      WebAuthnConfig

	// Email + password onboarding. The instance admin is a dedicated account
	// defined by AdminEmail/AdminPassword (mandatory whenever password auth is
	// enabled) and enforced at every boot; sign-ups are always regular users.
	PasswordEnabled   bool   // enable email/password registration + login
	RegistrationOpen  bool   // false = invite-only: sign-up requires an invite
	AdminEmail        string // dedicated instance admin account (mandatory with password auth)
	AdminPassword     string // authoritative at boot: resets the stored hash
	EmailVerification bool   // require email verification before first login
	SMTP              SMTPConfig
}

// SMTPConfig is the mail server for verification, invite and password-reset
// emails. With an empty Host nothing is sent and no link is ever logged:
// email verification then cannot be enabled, invite links are copied from
// Settings, and reset links are created by the instance admin.
type SMTPConfig struct {
	Host string
	Port string
	User string
	Pass string
	From string
}

type WebAuthnConfig struct {
	Enabled     bool
	RPID        string   // relying-party id (effective domain, e.g. openbeehive.org)
	RPOrigins   []string // allowed origins, e.g. https://app.openbeehive.org
	DisplayName string
}

type OIDCProvider struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	Scopes       []string
}

// --- env helpers (the only place env vars are read) ---

// envPrefix namespaces every runtime configuration variable, so all settings
// are read as BEEHIVE_<KEY> (e.g. BEEHIVE_DEPLOYMENT_PROFILE, BEEHIVE_DATABASE_DSN).
const envPrefix = "BEEHIVE_"

func env(key, def string) string {
	if v := os.Getenv(envPrefix + key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envPrefix + key))) {
	case "":
		return def
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(envPrefix + key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

// envList splits a comma-separated value into trimmed, non-empty items.
func envList(key string, def []string) []string {
	v := os.Getenv(envPrefix + key)
	if v == "" {
		return def
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}

// hostOf returns the host part of a base URL (for WebAuthn RP id defaults).
func hostOf(raw string) string {
	s := raw
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	return s
}

func Load() (*Config, error) {
	c := &Config{}
	// A .env file next to the binary configures it without a wrapper script;
	// variables already present in the environment win.
	envFile, err := loadDotEnv(".env")
	if err != nil {
		return nil, err
	}
	c.EnvFile = envFile
	c.Profile = env("DEPLOYMENT_PROFILE", "selfhost")
	c.Addr = env("ADDR", ":8080")
	c.PublicBaseURL = env("PUBLIC_BASE_URL", "http://localhost:8080")
	if err := loadStorage(c); err != nil {
		return nil, err
	}

	// --- HTTP server ---
	c.Server = ServerConfig{
		ReadHeaderTimeout: envDuration("HTTP_READ_HEADER_TIMEOUT", 10*time.Second),
		ReadTimeout:       envDuration("HTTP_READ_TIMEOUT", 2*time.Minute),
		WriteTimeout:      envDuration("HTTP_WRITE_TIMEOUT", 2*time.Minute),
		IdleTimeout:       envDuration("HTTP_IDLE_TIMEOUT", 120*time.Second),
		ShutdownTimeout:   envDuration("HTTP_SHUTDOWN_TIMEOUT", 15*time.Second),
	}

	// --- CORS: same-origin unless other origins are listed ---
	c.CORS = CORSConfig{
		AllowedOrigins:   envList("CORS_ALLOWED_ORIGINS", nil),
		AllowCredentials: envBool("CORS_ALLOW_CREDENTIALS", true),
	}
	for _, o := range c.CORS.AllowedOrigins {
		if o == "*" && c.CORS.AllowCredentials {
			// Any origin plus credentials would let every website act as the
			// signed-in user.
			c.CORS.AllowCredentials = false
			if os.Getenv(envPrefix+"CORS_ALLOW_CREDENTIALS") != "" {
				c.Warnings = append(c.Warnings, "BEEHIVE_CORS_ALLOWED_ORIGINS=* disables credentials; list the origins explicitly to allow them")
			}
		}
	}
	c.TrustProxyHeaders = envBool("TRUST_PROXY_HEADERS", false)
	c.AllowedHosts = envList("ALLOWED_HOSTS", nil)
	if err := loadMaps(c); err != nil {
		return nil, err
	}

	// --- Sync ---
	c.Sync = SyncConfig{
		// A fresh random id per boot keeps server-side change stamps distinct
		// from every other writer without any configuration.
		NodeID: env("NODE_ID", randomNodeID()),
	}
	if err := loadSync(c); err != nil {
		return nil, err
	}

	// --- Embedded web (single-binary production) ---
	c.Web = WebConfig{
		Serve: envBool("SERVE_WEB", true),
		Dir:   env("WEB_DIR", ""),
	}

	if err := loadAuth(c); err != nil {
		return nil, err
	}
	if err := loadOIDC(c); err != nil {
		return nil, err
	}
	if err := checkLogin(c); err != nil {
		return nil, err
	}
	return c, nil
}

// LoginEnabled reports whether any sign-in method is configured. Without
// one the server runs in single-user mode: every request acts as the local
// user.
func (c *Config) LoginEnabled() bool {
	return c.Auth.PasswordEnabled || c.Auth.WebAuthn.Enabled || len(c.OIDC.Enabled) > 0
}

// PublicHost returns the host name of BEEHIVE_PUBLIC_BASE_URL without port.
func (c *Config) PublicHost() string {
	u, err := url.Parse(c.PublicBaseURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func isLoopbackHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

// MinSessionSecretBytes is the shortest session secret the server accepts.
const MinSessionSecretBytes = 32

// checkLogin validates the settings every login method depends on.
func checkLogin(c *Config) error {
	if !c.LoginEnabled() {
		if !isLoopbackHost(c.PublicHost()) {
			c.Warnings = append(c.Warnings, fmt.Sprintf(
				"no login is configured, yet BEEHIVE_PUBLIC_BASE_URL is %s: everyone who can reach this server has full access; set BEEHIVE_PASSWORD_AUTH=true to require sign-in",
				c.PublicBaseURL))
		}
		return nil
	}
	const hint = "generate one with: openssl rand -base64 32"
	switch n := len(c.Auth.SessionSecret); {
	case n == 0:
		return fmt.Errorf("BEEHIVE_SESSION_SECRET is required when a login method is enabled; %s", hint)
	case n < MinSessionSecretBytes:
		return fmt.Errorf("BEEHIVE_SESSION_SECRET is too short (%d bytes, minimum %d); %s", n, MinSessionSecretBytes, hint)
	}
	if c.Auth.EmailVerification && c.Auth.SMTP.Host == "" {
		return fmt.Errorf("BEEHIVE_EMAIL_VERIFICATION=true needs a mail server: set BEEHIVE_SMTP_HOST, or turn verification off")
	}
	return nil
}

// loadSync reads the sync settings that are numbers.
func loadSync(c *Config) error {
	var err error
	if c.Sync.MinProtocol, err = envCount("SYNC_MIN_PROTOCOL", 0); err != nil {
		return fmt.Errorf("%w (the lowest sync protocol version accepted from apps; 0 accepts all)", err)
	}
	return nil
}

// envCount reads a whole number of 0 or more.
func envCount(key string, def int) (int, error) {
	v := strings.TrimSpace(os.Getenv(envPrefix + key))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s%s must be a whole number, 0 or more; got %q", envPrefix, key, v)
	}
	return n, nil
}

func randomNodeID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "srv-" + hex.EncodeToString(b)
}

// loadMaps reads the map settings and derives the origins the browser may
// contact for them.
func loadMaps(c *Config) error {
	switch v := strings.ToLower(strings.TrimSpace(env("MAPS", "on"))); v {
	case "on", "true", "1", "yes":
		c.Maps.Enabled = true
	case "off", "false", "0", "no":
		return nil
	default:
		return fmt.Errorf("BEEHIVE_MAPS must be \"on\" or \"off\", not %q", v)
	}
	c.Maps.TileURL = env("MAP_TILE_URL", "https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png")
	c.Maps.GeocoderURL = strings.TrimRight(env("GEOCODER_URL", "https://nominatim.openstreetmap.org"), "/")
	var err error
	if c.Maps.TileOrigin, err = originOf(c.Maps.TileURL); err != nil {
		return fmt.Errorf("BEEHIVE_MAP_TILE_URL: %w", err)
	}
	if c.Maps.GeocoderOrigin, err = originOf(c.Maps.GeocoderURL); err != nil {
		return fmt.Errorf("BEEHIVE_GEOCODER_URL: %w", err)
	}
	return nil
}

// originOf returns scheme://host[:port] of an http(s) URL in the form a
// Content-Security-Policy source expects. A Leaflet "{s}" subdomain
// placeholder in the first host label becomes a wildcard label.
func originOf(raw string) (string, error) {
	u, err := url.Parse(strings.ReplaceAll(raw, "{s}", "*"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("%q is not an http(s) URL", raw)
	}
	host := u.Host
	if i := strings.IndexByte(host, '*'); i >= 0 {
		dot := strings.IndexByte(host, '.')
		if dot < i || strings.Contains(host[dot:], "*") {
			return "", fmt.Errorf("%q: {s} may only appear in the first label of the host name", raw)
		}
		host = "*" + host[dot:]
	}
	if strings.ContainsAny(host, " ;,'\"") {
		return "", fmt.Errorf("%q has an invalid host name", raw)
	}
	return u.Scheme + "://" + host, nil
}

// loadStorage derives the storage defaults from the profile, then lets the
// granular ENV variables override them.
func loadStorage(c *Config) error {
	switch c.Profile {
	case "cloud":
		c.DB.Driver = DriverPostgres
		c.DB.DSN = env("DATABASE_DSN", "postgres://openbeehive:openbeehive@localhost:5432/openbeehive?sslmode=disable")
		c.Blob.Backend = BlobMinIO
	case "selfhost":
		c.DB.Driver = DriverSQLite
		c.DB.DSN = env("DATABASE_DSN", "file:openbeehive.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
		c.Blob.Backend = BlobFS
	default:
		return fmt.Errorf("unknown BEEHIVE_DEPLOYMENT_PROFILE %q (cloud|selfhost)", c.Profile)
	}
	if v := os.Getenv(envPrefix + "DATABASE_DRIVER"); v != "" {
		c.DB.Driver = DBDriver(v)
	}
	if v := os.Getenv(envPrefix + "DATABASE_DSN"); v != "" {
		c.DB.DSN = v
	}
	if v := os.Getenv(envPrefix + "BLOB_BACKEND"); v != "" {
		c.Blob.Backend = BlobBackend(v)
	}
	switch c.DB.Driver {
	case DriverPostgres, DriverMySQL, DriverSQLite:
	default:
		return fmt.Errorf("unsupported BEEHIVE_DATABASE_DRIVER %q", c.DB.Driver)
	}

	// --- blob-specific options ---
	c.Blob.Endpoint = env("MINIO_ENDPOINT", "localhost:9000")
	c.Blob.AccessKey = env("MINIO_ACCESS_KEY", "minioadmin")
	c.Blob.SecretKey = env("MINIO_SECRET_KEY", "minioadmin")
	c.Blob.Bucket = env("MINIO_BUCKET", "openbeehive")
	c.Blob.UseSSL = envBool("MINIO_USE_SSL", false)
	c.Blob.BaseDir = env("BLOB_DIR", "./data/blobs")
	c.Blob.PublicURL = env("BLOB_PUBLIC_URL", c.PublicBaseURL+"/files")
	return nil
}

// loadAuth reads session, WebAuthn, password onboarding, SMTP, demo and admin settings.
func loadAuth(c *Config) error {
	c.Auth = AuthConfig{
		SessionSecret: env("SESSION_SECRET", ""),
		SessionTTL:    envDuration("SESSION_TTL", 720*time.Hour),
		WebAuthn: WebAuthnConfig{
			Enabled:     envBool("WEBAUTHN_ENABLED", false),
			RPID:        env("WEBAUTHN_RP_ID", hostOf(c.PublicBaseURL)),
			RPOrigins:   envList("WEBAUTHN_RP_ORIGINS", []string{c.PublicBaseURL}),
			DisplayName: env("WEBAUTHN_RP_DISPLAY_NAME", "Openbeehive"),
		},
	}
	// Email/password onboarding. Defaults on for the cloud profile (multi-user),
	// off for selfhost (single-user, no login); override with BEEHIVE_PASSWORD_AUTH.
	c.Auth.PasswordEnabled = envBool("PASSWORD_AUTH", c.Profile == "cloud")
	// Open self-registration. When false the instance is invite-only: sign-up
	// requires a valid invite; existing accounts sign in normally.
	c.Auth.RegistrationOpen = envBool("REGISTRATION", true)
	c.Auth.EmailVerification = envBool("EMAIL_VERIFICATION", false)
	c.Auth.SMTP = SMTPConfig{
		Host: env("SMTP_HOST", ""),
		Port: env("SMTP_PORT", "587"),
		User: env("SMTP_USER", ""),
		Pass: env("SMTP_PASS", ""),
		From: env("SMTP_FROM", "Openbeehive <no-reply@openbeehive.org>"),
	}

	// Demo tenant (off by default). Enabling it implies password auth so the
	// demo account can sign in. AutoLogin signs anonymous visitors into the
	// demo automatically, for pure-showcase hosts; leave it off on a combined
	// production+demo instance so real users get the login screen (with the
	// demo button on it).
	c.Demo = DemoConfig{
		Enabled:   envBool("DEMO", false),
		AutoLogin: envBool("DEMO_AUTOLOGIN", false),
		Email:     strings.TrimSpace(strings.ToLower(env("DEMO_EMAIL", "demo@app.openbeehive.org"))),
		Password:  env("DEMO_PASSWORD", "demo"),
	}
	if c.Demo.Enabled {
		c.Auth.PasswordEnabled = true
	}

	// Dedicated instance admin. Mandatory whenever a login exists (password
	// auth enabled; single-user no-auth self-hosting needs none): admin is
	// never granted any other way, and the env values are authoritative at
	// every boot (see auth.EnsureAdmin; this doubles as password recovery).
	c.Auth.AdminEmail = strings.TrimSpace(strings.ToLower(env("ADMIN_EMAIL", "")))
	c.Auth.AdminPassword = env("ADMIN_PASSWORD", "")
	if !c.Auth.PasswordEnabled {
		return nil
	}
	switch {
	case c.Auth.AdminEmail == "" || c.Auth.AdminPassword == "":
		return fmt.Errorf("BEEHIVE_ADMIN_EMAIL and BEEHIVE_ADMIN_PASSWORD are required when password auth is enabled (it is implied by BEEHIVE_DEMO=true and the cloud profile)")
	case len(c.Auth.AdminPassword) < 8:
		return fmt.Errorf("BEEHIVE_ADMIN_PASSWORD must be at least 8 characters")
	case strings.EqualFold(c.Auth.AdminEmail, c.Demo.Email):
		return fmt.Errorf("BEEHIVE_ADMIN_EMAIL must not be the demo account email")
	}
	return nil
}

// loadOIDC reads one issuer/client id/client secret triple per provider, e.g.
// BEEHIVE_OIDC_GOOGLE_ISSUER, BEEHIVE_OIDC_GOOGLE_CLIENT_ID, BEEHIVE_OIDC_GOOGLE_CLIENT_SECRET.
func loadOIDC(c *Config) error {
	c.OIDC.RedirectURL = env("OIDC_REDIRECT_URL", c.PublicBaseURL+"/auth/callback")
	c.OIDC.Providers = map[string]OIDCProvider{}
	for _, name := range envList("OIDC_PROVIDERS", nil) {
		name = strings.ToLower(name)
		p := strings.ToUpper(name)
		c.OIDC.Enabled = append(c.OIDC.Enabled, name)
		c.OIDC.Providers[name] = OIDCProvider{
			IssuerURL:    os.Getenv(envPrefix + "OIDC_" + p + "_ISSUER"),
			ClientID:     os.Getenv(envPrefix + "OIDC_" + p + "_CLIENT_ID"),
			ClientSecret: os.Getenv(envPrefix + "OIDC_" + p + "_CLIENT_SECRET"),
			Scopes:       envList("OIDC_"+p+"_SCOPES", []string{"openid", "profile", "email"}),
		}
		if pc := c.OIDC.Providers[name]; pc.IssuerURL == "" || pc.ClientID == "" {
			return fmt.Errorf("OIDC provider %q is enabled but not configured: set BEEHIVE_OIDC_%s_ISSUER and BEEHIVE_OIDC_%s_CLIENT_ID, or remove it from BEEHIVE_OIDC_PROVIDERS", name, p, p)
		}
	}
	return nil
}
