package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

// clearBeehiveEnv guards against BEEHIVE_* leaking in from the host shell.
func clearBeehiveEnv(t *testing.T) {
	t.Helper()
	for _, v := range []string{"DEPLOYMENT_PROFILE", "PASSWORD_AUTH", "DEMO",
		"ADMIN_EMAIL", "ADMIN_PASSWORD", "REGISTRATION", "DEMO_EMAIL", "DEMO_AUTOLOGIN",
		"SESSION_SECRET", "WEBAUTHN_ENABLED", "OIDC_PROVIDERS", "OIDC_GOOGLE_ISSUER",
		"OIDC_GOOGLE_CLIENT_ID", "EMAIL_VERIFICATION", "SMTP_HOST", "PUBLIC_BASE_URL",
		"CORS_ALLOWED_ORIGINS", "CORS_ALLOW_CREDENTIALS", "NODE_ID", "MAPS", "MAP_TILE_URL",
		"GEOCODER_URL", "ALLOWED_HOSTS", "TRUST_PROXY_HEADERS", "HTTP_READ_TIMEOUT",
		"HTTP_WRITE_TIMEOUT"} {
		t.Setenv(envPrefix+v, "")
	}
	// Load reads .env from the working directory; tests must not pick one up.
	t.Chdir(t.TempDir())
}

// validSecret is a session secret of the minimum accepted length.
var validSecret = strings.Repeat("s", MinSessionSecretBytes)

func TestLoadRequiresAdminWithPasswordAuth(t *testing.T) {
	clearBeehiveEnv(t)
	t.Setenv(envPrefix+"DEPLOYMENT_PROFILE", "selfhost")
	t.Setenv(envPrefix+"PASSWORD_AUTH", "true")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "BEEHIVE_ADMIN_EMAIL") {
		t.Fatalf("want missing-admin error, got %v", err)
	}

	t.Setenv(envPrefix+"ADMIN_EMAIL", "admin@example.org")
	t.Setenv(envPrefix+"ADMIN_PASSWORD", "short")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "8 characters") {
		t.Fatalf("want short-password error, got %v", err)
	}

	t.Setenv(envPrefix+"ADMIN_PASSWORD", "password123")
	t.Setenv(envPrefix+"SESSION_SECRET", validSecret)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("valid admin config: %v", err)
	}
	if cfg.Auth.AdminEmail != "admin@example.org" {
		t.Fatalf("admin email = %q", cfg.Auth.AdminEmail)
	}
}

func TestLoadDemoImpliesAdminRequirement(t *testing.T) {
	clearBeehiveEnv(t)
	t.Setenv(envPrefix+"DEPLOYMENT_PROFILE", "selfhost")
	t.Setenv(envPrefix+"DEMO", "true") // implies password auth

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "BEEHIVE_ADMIN_EMAIL") {
		t.Fatalf("want missing-admin error, got %v", err)
	}

	// The admin must not shadow the demo account.
	t.Setenv(envPrefix+"ADMIN_EMAIL", "demo@app.openbeehive.org")
	t.Setenv(envPrefix+"ADMIN_PASSWORD", "password123")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "demo account") {
		t.Fatalf("want admin-is-demo error, got %v", err)
	}
}

func TestLoadNoAuthNeedsNoAdmin(t *testing.T) {
	clearBeehiveEnv(t)
	t.Setenv(envPrefix+"DEPLOYMENT_PROFILE", "selfhost")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("single-user selfhost must not require an admin: %v", err)
	}
	if cfg.Auth.PasswordEnabled {
		t.Fatalf("selfhost default should have password auth off")
	}
}

// enableLogin sets the minimum for a password-auth instance.
func enableLogin(t *testing.T) {
	t.Helper()
	t.Setenv(envPrefix+"PASSWORD_AUTH", "true")
	t.Setenv(envPrefix+"ADMIN_EMAIL", "admin@example.org")
	t.Setenv(envPrefix+"ADMIN_PASSWORD", "password123")
}

func TestLoadRequiresStrongSessionSecretWithLogin(t *testing.T) {
	clearBeehiveEnv(t)
	enableLogin(t)

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "BEEHIVE_SESSION_SECRET is required") ||
		!strings.Contains(err.Error(), "openssl rand -base64 32") {
		t.Fatalf("unset secret: want an error that says how to generate one, got %v", err)
	}
	t.Setenv(envPrefix+"SESSION_SECRET", "too-short")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "too short") ||
		!strings.Contains(err.Error(), "openssl rand -base64 32") {
		t.Fatalf("short secret: got %v", err)
	}
	t.Setenv(envPrefix+"SESSION_SECRET", validSecret)
	if _, err := Load(); err != nil {
		t.Fatalf("32-byte secret must be accepted: %v", err)
	}

	// Passkeys or OIDC alone are login methods too.
	clearBeehiveEnv(t)
	t.Setenv(envPrefix+"WEBAUTHN_ENABLED", "true")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "BEEHIVE_SESSION_SECRET") {
		t.Fatalf("passkeys without secret: got %v", err)
	}
}

func TestLoadNoLoginNeedsNoSecretButWarnsWhenPublic(t *testing.T) {
	clearBeehiveEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("no-login default: %v", err)
	}
	if cfg.LoginEnabled() || len(cfg.Warnings) != 0 {
		t.Fatalf("localhost single-user mode must be silent: login=%v warnings=%v", cfg.LoginEnabled(), cfg.Warnings)
	}
	t.Setenv(envPrefix+"PUBLIC_BASE_URL", "https://bees.example.org")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "no login is configured") {
		t.Fatalf("public URL without login must warn: %v", cfg.Warnings)
	}
}

func TestLoadCORSDefaultsToSameOrigin(t *testing.T) {
	clearBeehiveEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.CORS.AllowedOrigins) != 0 {
		t.Fatalf("default must allow no foreign origin, got %v", cfg.CORS.AllowedOrigins)
	}

	// Any origin never comes with credentials, whatever the operator sets.
	t.Setenv(envPrefix+"CORS_ALLOWED_ORIGINS", "*")
	t.Setenv(envPrefix+"CORS_ALLOW_CREDENTIALS", "true")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CORS.AllowCredentials {
		t.Fatal("wildcard origin must disable credentials")
	}
	if len(cfg.Warnings) == 0 || !strings.Contains(cfg.Warnings[0], "BEEHIVE_CORS_ALLOWED_ORIGINS") {
		t.Fatalf("the override must be reported: %v", cfg.Warnings)
	}

	t.Setenv(envPrefix+"CORS_ALLOWED_ORIGINS", "https://app.example.org")
	if cfg, _ = Load(); !cfg.CORS.AllowCredentials {
		t.Fatal("explicit origins keep credentials")
	}
}

func TestLoadNodeIDIsRandomByDefault(t *testing.T) {
	clearBeehiveEnv(t)
	a, _ := Load()
	b, _ := Load()
	if a.Sync.NodeID == "server" || a.Sync.NodeID == "" || a.Sync.NodeID == b.Sync.NodeID {
		t.Fatalf("node ids must be random per boot: %q, %q", a.Sync.NodeID, b.Sync.NodeID)
	}
	t.Setenv(envPrefix+"NODE_ID", "fixed")
	if c, _ := Load(); c.Sync.NodeID != "fixed" {
		t.Fatalf("explicit node id ignored: %q", c.Sync.NodeID)
	}
}

func TestLoadTimeoutsAreBoundedByDefault(t *testing.T) {
	clearBeehiveEnv(t)
	cfg, _ := Load()
	if cfg.Server.ReadTimeout != 2*time.Minute || cfg.Server.WriteTimeout != 2*time.Minute {
		t.Fatalf("timeouts: read=%v write=%v", cfg.Server.ReadTimeout, cfg.Server.WriteTimeout)
	}
}

func TestLoadMaps(t *testing.T) {
	clearBeehiveEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	m := cfg.Maps
	if !m.Enabled || m.TileURL != "https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png" ||
		m.GeocoderURL != "https://nominatim.openstreetmap.org" ||
		m.TileOrigin != "https://*.tile.openstreetmap.org" || m.GeocoderOrigin != "https://nominatim.openstreetmap.org" {
		t.Fatalf("defaults: %+v", m)
	}

	t.Setenv(envPrefix+"MAP_TILE_URL", "https://tiles.example.org:8443/{z}/{x}/{y}.png")
	t.Setenv(envPrefix+"GEOCODER_URL", "https://geo.example.org/nominatim/")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Maps.TileOrigin != "https://tiles.example.org:8443" || cfg.Maps.GeocoderOrigin != "https://geo.example.org" ||
		cfg.Maps.GeocoderURL != "https://geo.example.org/nominatim" {
		t.Fatalf("custom: %+v", cfg.Maps)
	}

	t.Setenv(envPrefix+"MAPS", "off")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Maps.Enabled || cfg.Maps.TileURL != "" || cfg.Maps.TileOrigin != "" || cfg.Maps.GeocoderURL != "" {
		t.Fatalf("off must clear everything: %+v", cfg.Maps)
	}

	t.Setenv(envPrefix+"MAPS", "maybe")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "BEEHIVE_MAPS") {
		t.Fatalf("bad switch: %v", err)
	}
	t.Setenv(envPrefix+"MAPS", "on")
	t.Setenv(envPrefix+"MAP_TILE_URL", "tiles.example.org/{z}")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "BEEHIVE_MAP_TILE_URL") {
		t.Fatalf("bad tile url: %v", err)
	}
	t.Setenv(envPrefix+"MAP_TILE_URL", "https://tile.{s}.example.org/{z}")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "first label") {
		t.Fatalf("wildcard outside the first label: %v", err)
	}
}

func TestLoadRejectsUnconfiguredOIDCProvider(t *testing.T) {
	clearBeehiveEnv(t)
	t.Setenv(envPrefix+"OIDC_PROVIDERS", "google")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "BEEHIVE_OIDC_GOOGLE_CLIENT_ID") ||
		!strings.Contains(err.Error(), "BEEHIVE_OIDC_PROVIDERS") {
		t.Fatalf("want an error naming the variables, got %v", err)
	}
}

func TestLoadEmailVerificationNeedsSMTP(t *testing.T) {
	clearBeehiveEnv(t)
	enableLogin(t)
	t.Setenv(envPrefix+"SESSION_SECRET", validSecret)
	t.Setenv(envPrefix+"EMAIL_VERIFICATION", "true")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "BEEHIVE_SMTP_HOST") {
		t.Fatalf("verification without SMTP: %v", err)
	}
	t.Setenv(envPrefix+"SMTP_HOST", "mail.example.org")
	if _, err := Load(); err != nil {
		t.Fatalf("verification with SMTP: %v", err)
	}
}

func TestLoadAppliesDotEnv(t *testing.T) {
	clearBeehiveEnv(t) // also moves into an empty directory
	// Variables this test lets the file define must be truly unset, not empty.
	for _, k := range []string{"ADDR", "PUBLIC_BASE_URL", "NODE_ID", "ALLOWED_HOSTS", "SMTP_FROM"} {
		old, had := os.LookupEnv(envPrefix + k)
		_ = os.Unsetenv(envPrefix + k)
		t.Cleanup(func() {
			if had {
				_ = os.Setenv(envPrefix+k, old)
			} else {
				_ = os.Unsetenv(envPrefix + k)
			}
		})
	}
	t.Setenv(envPrefix+"ADDR", ":9999") // the environment wins over the file

	env := strings.Join([]string{
		"# comment",
		"",
		"BEEHIVE_ADDR=:1234",
		"BEEHIVE_PUBLIC_BASE_URL=http://localhost:1234   # trailing comment",
		"export BEEHIVE_NODE_ID=\"from file\"",
		"BEEHIVE_ALLOWED_HOSTS='bees.lan, hive.lan'",
		"BEEHIVE_SMTP_FROM=Openbeehive <no-reply@example.org>",
	}, "\n")
	if err := os.WriteFile(".env", []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load with .env: %v", err)
	}
	if !cfg.EnvFile {
		t.Fatal(".env not reported as applied")
	}
	if cfg.Addr != ":9999" {
		t.Errorf("existing environment must win: addr=%q", cfg.Addr)
	}
	if cfg.PublicBaseURL != "http://localhost:1234" {
		t.Errorf("trailing comment not stripped: %q", cfg.PublicBaseURL)
	}
	if cfg.Sync.NodeID != "from file" {
		t.Errorf("export + double quotes: %q", cfg.Sync.NodeID)
	}
	if len(cfg.AllowedHosts) != 2 || cfg.AllowedHosts[1] != "hive.lan" {
		t.Errorf("single quotes: %v", cfg.AllowedHosts)
	}
	if cfg.Auth.SMTP.From != "Openbeehive <no-reply@example.org>" {
		t.Errorf("unquoted value with spaces: %q", cfg.Auth.SMTP.From)
	}

	if err := os.WriteFile(".env", []byte("this is not an assignment\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), ".env line 1") {
		t.Fatalf("malformed .env must name the line: %v", err)
	}
}

func TestLoadWithoutDotEnv(t *testing.T) {
	clearBeehiveEnv(t)
	cfg, err := Load()
	if err != nil || cfg.EnvFile {
		t.Fatalf("no .env: err=%v applied=%v", err, cfg.EnvFile)
	}
}
