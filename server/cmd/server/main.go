package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"strings"
	"syscall"

	"connectrpc.com/connect"
	"github.com/rs/cors"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	"github.com/openbeehive-org/openbeehive-app/server/internal/demo"
	"github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1/openbeehivev1connect"
	"github.com/openbeehive-org/openbeehive-app/server/internal/service"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage/blob"
	sqlstore "github.com/openbeehive-org/openbeehive-app/server/internal/storage/sql"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
	"github.com/openbeehive-org/openbeehive-app/server/internal/web"
)

func main() {
	ctx := context.Background()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	log.Printf("Openbeehive · profile=%s · DB=%s · Blob=%s", cfg.Profile, cfg.DB.Driver, cfg.Blob.Backend)

	store := openStore(ctx, cfg)
	defer func() { _ = store.Close() }()

	blobs, err := blob.New(cfg)
	if err != nil {
		log.Fatalf("blob: %v", err)
	}

	mux := http.NewServeMux()
	interceptors := setupAuth(ctx, cfg, store, blobs, mux)
	registerServices(mux, cfg, store, interceptors)
	registerWeb(mux, cfg)

	// CORS for browser clients; Connect-Web needs these headers.
	handler := cors.New(cors.Options{
		AllowedOrigins:   cfg.CORS.AllowedOrigins,
		AllowedMethods:   []string{http.MethodGet, http.MethodPost, http.MethodOptions},
		AllowedHeaders:   []string{"*"},
		ExposedHeaders:   []string{"Connect-Protocol-Version", "Grpc-Status", "Grpc-Message"},
		AllowCredentials: cfg.CORS.AllowCredentials,
	}).Handler(mux)
	serve(ctx, cfg, handler)
}

// openStore opens the database, migrates it and seeds what the profile needs.
func openStore(ctx context.Context, cfg *config.Config) *sqlstore.Store {
	store, err := sqlstore.Open(cfg)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		log.Fatalf("migration: %v", err)
	}

	// Dedicated instance admin (BEEHIVE_ADMIN_EMAIL/PASSWORD, mandatory with
	// password auth). Created or repaired on every boot; sign-up never grants
	// the admin role.
	if cfg.Auth.PasswordEnabled {
		if err := auth.EnsureAdmin(ctx, store.Users(), cfg); err != nil {
			log.Fatalf("auth: %v", err)
		}
		log.Printf("auth: instance admin ensured (%s)", cfg.Auth.AdminEmail)
	}

	// Demo tenant (off by default; BEEHIVE_DEMO=true). Seeds 15 hives across 4
	// apiaries and re-seeds hourly so the showcase stays consistent.
	if cfg.Demo.Enabled {
		if err := demo.New(store.DB(), wsync.NewHLC("demo"), cfg).Install(ctx); err != nil {
			log.Printf("demo: install failed: %v", err)
		}
	}
	return store
}

// setupAuth mounts the auth endpoints (session, password, OIDC, WebAuthn,
// API keys) and returns the interceptors the RPC handlers run behind.
func setupAuth(ctx context.Context, cfg *config.Config, store *sqlstore.Store, blobs blob.Store, mux *http.ServeMux) []connect.Interceptor {
	sessions := auth.NewSessionManager(cfg.Auth.SessionSecret, cfg.Auth.SessionTTL)
	authEnabled := len(cfg.OIDC.Enabled) > 0 || cfg.Auth.WebAuthn.Enabled || cfg.Auth.PasswordEnabled

	// Provisioner: creates/links users and their tenants (organizations).
	prov := auth.NewProvisioner(store.Users(), store.Orgs(), store.Members())

	if !authEnabled {
		// Self-host single-user mode: no auth configured -> inject a fixed
		// local identity so the offline-first app works without login.
		log.Printf("auth: none configured, single-user mode (user=%s)", auth.LocalUser.UserID)
		return []connect.Interceptor{auth.DevInterceptor()}
	}

	// Multi-tenant endpoints (me / switch / create / invite / accept).
	auth.NewTenantAPI(sessions, store.Users(), store.Orgs(), store.Members(), store.Invites(), prov, cfg, blobs).Routes(mux)

	// Email + password sign-up (verification optional). The instance admin is
	// not created here; auth.EnsureAdmin creates it from BEEHIVE_ADMIN_EMAIL /
	// BEEHIVE_ADMIN_PASSWORD on boot.
	if cfg.Auth.PasswordEnabled {
		auth.NewPasswordAuth(store.Users(), store.Invites(), sessions, cfg, prov).Routes(mux)
		log.Printf("auth: email/password onboarding enabled (verification=%v, open registration=%v)",
			cfg.Auth.EmailVerification, cfg.Auth.RegistrationOpen)
	}
	if len(cfg.OIDC.Enabled) > 0 {
		am, err := auth.NewManager(ctx, cfg)
		if err != nil {
			log.Fatalf("oidc: %v", err)
		}
		log.Printf("auth: OIDC providers active: %v", am.Names())
		mux.HandleFunc("/auth/login", am.LoginHandler())
		// Callback provisions/links an in-app user record (by subject, then email).
		mux.HandleFunc("/auth/callback", am.CallbackHandler(sessions, store.Users(), prov, cfg))
	}
	if cfg.Auth.WebAuthn.Enabled {
		wa, err := auth.NewWebAuthn(cfg, store.DB(), store.Users(), prov, sessions)
		if err != nil {
			log.Fatalf("webauthn: %v", err)
		}
		log.Printf("auth: WebAuthn enabled (rp=%s)", cfg.Auth.WebAuthn.RPID)
		wa.Routes(mux)
	}

	mux.HandleFunc("/auth/logout", sessions.LogoutHandler())
	// API keys for scripts and integrations: managed with a session in
	// Settings, accepted on RPCs alongside session tokens.
	keys := auth.NewAPIKeys(store.DB(), sessions, store.Users(), store.Members())
	keys.Routes(mux)
	// Demo sessions and read-scoped API keys are read-only: reject mutating
	// RPCs server-side.
	return []connect.Interceptor{auth.SessionInterceptor(keys), auth.ReadOnlyGuard()}
}

// registerServices mounts the Connect-RPC handlers, blob serving and health.
func registerServices(mux *http.ServeMux, cfg *config.Config, store *sqlstore.Store, interceptors []connect.Interceptor) {
	opts := connect.WithInterceptors(interceptors...)

	// One clock for every server-side write, shared with the sync endpoint.
	hlc := wsync.NewHLC(cfg.Sync.NodeID)
	// Offline sync: central push/pull endpoint (the app's write path).
	mux.Handle(openbeehivev1connect.NewSyncServiceHandler(
		service.NewSyncService(store.DB(), hlc), opts))
	// CRUD services for scripts and integrations. Their writes go through the
	// same merge + change log as Push, so they show up on devices.
	w := service.NewWriter(store.DB(), hlc)
	mux.Handle(openbeehivev1connect.NewApiaryServiceHandler(service.NewApiaryService(store.Apiaries(), w), opts))
	mux.Handle(openbeehivev1connect.NewHiveServiceHandler(service.NewHiveService(store.Hives(), w), opts))
	mux.Handle(openbeehivev1connect.NewQueenServiceHandler(service.NewQueenService(w), opts))
	mux.Handle(openbeehivev1connect.NewInspectionServiceHandler(service.NewInspectionService(w), opts))
	mux.Handle(openbeehivev1connect.NewTaskServiceHandler(service.NewTaskService(w), opts))
	mux.Handle(openbeehivev1connect.NewTreatmentServiceHandler(service.NewTreatmentService(w), opts))
	mux.Handle(openbeehivev1connect.NewHarvestServiceHandler(service.NewHarvestService(w), opts))
	mux.Handle(openbeehivev1connect.NewEventServiceHandler(service.NewEventService(w), opts))
	mux.Handle(openbeehivev1connect.NewStatsServiceHandler(service.NewStatsService(w), opts))

	// FS blob serving in self-host mode (files only, no directory listings).
	if cfg.Blob.Backend == config.BlobFS {
		mux.Handle("/files/", http.StripPrefix("/files/",
			noDirListing(http.FileServer(http.Dir(cfg.Blob.BaseDir)))))
	}

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

// registerWeb mounts the embedded SPA (single-binary production) as the catch-all.
func registerWeb(mux *http.ServeMux, cfg *config.Config) {
	if !cfg.Web.Serve {
		return
	}
	h, err := web.Handler(cfg)
	if err != nil {
		log.Printf("web: not serving embedded app (%v) — run vite separately in dev", err)
		return
	}
	mux.Handle("/", h)
	log.Printf("web: serving embedded app at /")
}

// serve runs the HTTP server until SIGINT/SIGTERM, then shuts down gracefully.
func serve(ctx context.Context, cfg *config.Config, handler http.Handler) {
	// Unencrypted HTTP/2 (h2c) allows gRPC without TLS; TLS terminates at the reverse proxy.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		Protocols:         protocols,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
	}

	runCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		log.Printf("listening on %s", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-runCtx.Done()
	log.Printf("shutting down…")
	shutCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// noDirListing hides directory indexes from the blob file server.
func noDirListing(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "" || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}
