// Package config reads the runtime configuration from environment variables.
//
// Core idea: a single binary serves both worlds:
//   - cloud:    PostgreSQL + MinIO   (central public service)
//   - selfhost: SQLite   + filesystem (single beekeeper, single server)
//
// BEEHIVE_DEPLOYMENT_PROFILE sets defaults; every option can be overridden. There are
// no hard-coded operational values elsewhere in the server — everything the
// process needs at runtime is read here, once, into this struct at bootstrap.
package config

import (
	"fmt"
	"os"
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

type ServerConfig struct {
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration // 0 = no limit (streaming endpoints)
	WriteTimeout      time.Duration // 0 = no limit (streaming endpoints)
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
}

type CORSConfig struct {
	AllowedOrigins   []string
	AllowCredentials bool
}

type SyncConfig struct {
	NodeID       string // HLC node id stamped on server-side changes
	DefaultLimit int    // pull page size default
	MaxLimit     int    // pull page size cap
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

// SMTPConfig is used to send verification emails. If Host is empty, the server
// logs the verification link instead of sending mail (handy in dev).
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
	c.Profile = env("DEPLOYMENT_PROFILE", "selfhost")
	c.Addr = env("ADDR", ":8080")
	c.PublicBaseURL = env("PUBLIC_BASE_URL", "http://localhost:8080")
	if err := loadStorage(c); err != nil {
		return nil, err
	}

	// --- HTTP server ---
	c.Server = ServerConfig{
		ReadHeaderTimeout: envDuration("HTTP_READ_HEADER_TIMEOUT", 10*time.Second),
		ReadTimeout:       envDuration("HTTP_READ_TIMEOUT", 0),
		WriteTimeout:      envDuration("HTTP_WRITE_TIMEOUT", 0),
		IdleTimeout:       envDuration("HTTP_IDLE_TIMEOUT", 120*time.Second),
		ShutdownTimeout:   envDuration("HTTP_SHUTDOWN_TIMEOUT", 15*time.Second),
	}

	// --- CORS (Connect-Web needs these headers) ---
	c.CORS = CORSConfig{
		AllowedOrigins:   envList("CORS_ALLOWED_ORIGINS", []string{"*"}),
		AllowCredentials: envBool("CORS_ALLOW_CREDENTIALS", true),
	}

	// --- Sync ---
	c.Sync = SyncConfig{
		NodeID:       env("NODE_ID", "server"),
		DefaultLimit: 200,
		MaxLimit:     500,
	}

	// --- Embedded web (single-binary production) ---
	c.Web = WebConfig{
		Serve: envBool("SERVE_WEB", true),
		Dir:   env("WEB_DIR", ""),
	}

	if err := loadAuth(c); err != nil {
		return nil, err
	}
	loadOIDC(c)
	return c, nil
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
	// off for selfhost (single-user, no login) — override with BEEHIVE_PASSWORD_AUTH.
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
	// demo automatically — for pure-showcase hosts; leave it off on a combined
	// production+demo instance so real users get the login screen (with the
	// demo button on it).
	c.Demo = DemoConfig{
		Enabled:   envBool("DEMO", false),
		AutoLogin: envBool("DEMO_AUTOLOGIN", false),
		Email:     env("DEMO_EMAIL", "demo@app.openbeehive.org"),
		Password:  env("DEMO_PASSWORD", "demo"),
	}
	if c.Demo.Enabled {
		c.Auth.PasswordEnabled = true
	}

	// Dedicated instance admin. Mandatory whenever a login exists (password
	// auth enabled — single-user no-auth self-hosting needs none): admin is
	// never granted any other way, and the env values are authoritative at
	// every boot (see auth.EnsureAdmin — this doubles as password recovery).
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
func loadOIDC(c *Config) {
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
	}
}
