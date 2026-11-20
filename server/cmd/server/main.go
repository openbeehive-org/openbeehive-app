package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os/signal"
	"strings"
	"syscall"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/rs/cors"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

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
	if cfg.EnvFile {
		log.Printf("config: applied .env from the working directory (existing environment variables win)")
	}
	log.Printf("Openbeehive · profile=%s · DB=%s · Blob=%s", cfg.Profile, cfg.DB.Driver, cfg.Blob.Backend)
	logAuthMode(cfg)
	for _, w := range cfg.Warnings {
		log.Printf("WARNING: %s", w)
	}

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

	serve(ctx, cfg, harden(cfg, mux))
}

// logAuthMode states at boot who can use this server, the one fact an
// operator must not get wrong.
func logAuthMode(cfg *config.Config) {
	if !cfg.LoginEnabled() {
		hosts := []string{"localhost", "IP addresses"}
		if h := cfg.PublicHost(); h != "" && h != "localhost" && net.ParseIP(h) == nil {
			hosts = append(hosts, h)
		}
		hosts = append(hosts, cfg.AllowedHosts...)
		log.Printf("auth: NO LOGIN (single-user mode): every request has full access to all data")
		log.Printf("auth: accepting requests for hosts: %s (add names with BEEHIVE_ALLOWED_HOSTS)", strings.Join(hosts, ", "))
		return
	}
	var methods []string
	if cfg.Auth.PasswordEnabled {
		methods = append(methods, "email+password")
	}
	if cfg.Auth.WebAuthn.Enabled {
		methods = append(methods, "passkeys")
	}
	for _, p := range cfg.OIDC.Enabled {
		methods = append(methods, "OIDC:"+p)
	}
	log.Printf("auth: LOGIN REQUIRED (%s)", strings.Join(methods, ", "))
}

// harden wraps the routes in the cross-cutting HTTP rules: security headers
// on every response, the Host check of single-user mode, CORS for the
// configured origins, and no timeouts for streaming RPCs.
func harden(cfg *config.Config, mux http.Handler) http.Handler {
	handler := web.NoDeadline(streamingProcedures())(mux)

	// Same-origin unless origins are configured: without the CORS layer no
	// other site can read responses or send non-simple requests.
	if len(cfg.CORS.AllowedOrigins) > 0 {
		handler = cors.New(cors.Options{
			AllowedOrigins:   cfg.CORS.AllowedOrigins,
			AllowedMethods:   []string{http.MethodGet, http.MethodPost, http.MethodOptions},
			AllowedHeaders:   []string{"*"},
			ExposedHeaders:   []string{"Connect-Protocol-Version", "Grpc-Status", "Grpc-Message"},
			AllowCredentials: cfg.CORS.AllowCredentials,
		}).Handler(handler)
	}
	if !cfg.LoginEnabled() {
		handler = web.HostGuard(cfg)(handler)
	}
	var index []byte
	if cfg.Web.Serve {
		index, _ = web.IndexHTML(cfg) // not built: no inline script to allow
	}
	return web.SecurityHeaders(cfg, index)(handler)
}

// streamingProcedures lists the request paths of every streaming RPC in the
// API, taken from the proto descriptors.
func streamingProcedures() map[string]bool {
	paths := map[string]bool{}
	protoregistry.GlobalFiles.RangeFilesByPackage("openbeehive.v1", func(fd protoreflect.FileDescriptor) bool {
		services := fd.Services()
		for i := range services.Len() {
			svc := services.Get(i)
			methods := svc.Methods()
			for j := range methods.Len() {
				if m := methods.Get(j); m.IsStreamingServer() || m.IsStreamingClient() {
					paths["/"+string(svc.FullName())+"/"+string(m.Name())] = true
				}
			}
		}
		return true
	})
	return paths
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
	// Login enabled on an instance that ran in single-user mode: hand the
	// existing records to the instance admin so they do not vanish.
	if cfg.LoginEnabled() {
		prov := auth.NewProvisioner(store.Users(), store.Orgs(), store.Members())
		if err := auth.AdoptLocalData(ctx, store, store.Users(), store.Members(), prov, cfg); err != nil {
			log.Printf("auth: %v (the records are untouched; the move is retried at the next start)", err)
		}
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
func setupAuth(ctx context.Context, cfg *config.Config, store *sqlstore.Store, blobs blob.Store, mux *http.ServeMux) []connect.ServerInterceptor {
	// Every plain-HTTP auth and workspace endpoint goes through one router,
	// which enforces methods, the cross-site check and the body limit.
	rt := auth.NewRouter(cfg)
	mux.Handle("/auth/", rt)
	mux.Handle("/tenants/", rt)
	rt.Get("/auth/instance", auth.InstanceHandler(cfg))

	if !cfg.LoginEnabled() {
		// Self-host single-user mode: no auth configured -> inject a fixed
		// local identity so the offline-first app works without login.
		return []connect.ServerInterceptor{auth.DevInterceptor()}
	}

	sessions, err := auth.NewSessionManager(cfg.Auth.SessionSecret, cfg.Auth.SessionTTL, store.Users(), store.Members())
	if err != nil {
		log.Fatalf("auth: %v", err)
	}
	throttle := auth.NewThrottle(cfg.TrustProxyHeaders)

	// Provisioner: creates/links users and their tenants (organizations).
	prov := auth.NewProvisioner(store.Users(), store.Orgs(), store.Members())

	// Multi-tenant endpoints (me / switch / create / invite / accept).
	tenants := auth.NewTenantAPI(sessions, store.Users(), store.Orgs(), store.Members(), store.Invites(), prov, cfg, blobs)
	tenants.Routes(rt)
	// The signed-in account itself: deletion, and a new session after losing
	// the active workspace.
	reauth := auth.NewReauthStore()
	auth.NewAccountAPI(tenants, store, throttle, reauth).Routes(rt)

	// Email + password sign-up (verification optional). The instance admin is
	// not created here; auth.EnsureAdmin creates it from BEEHIVE_ADMIN_EMAIL /
	// BEEHIVE_ADMIN_PASSWORD on boot.
	if cfg.Auth.PasswordEnabled {
		auth.NewPasswordAuth(store.Users(), store.Invites(), store, sessions, cfg, prov, throttle).Routes(rt)
		log.Printf("auth: email/password onboarding enabled (verification=%v, open registration=%v)",
			cfg.Auth.EmailVerification, cfg.Auth.RegistrationOpen)
		if cfg.Auth.SMTP.Host == "" {
			log.Printf("auth: no mail server (BEEHIVE_SMTP_HOST): forgotten passwords are reset with a link the instance admin creates in Settings")
		}
	}
	if len(cfg.OIDC.Enabled) > 0 {
		am, err := auth.NewManager(ctx, cfg)
		if err != nil {
			log.Fatalf("oidc: %v", err)
		}
		log.Printf("auth: OIDC providers active: %v", am.Names())
		// The callback provisions an in-app user record, or links one whose
		// email both sides have verified.
		am.Routes(rt, sessions, store.Users(), prov, cfg)
	}
	if cfg.Auth.WebAuthn.Enabled {
		wa, err := auth.NewWebAuthn(cfg, store.DB(), store.Users(), prov, sessions, throttle, reauth)
		if err != nil {
			log.Fatalf("webauthn: %v", err)
		}
		log.Printf("auth: WebAuthn enabled (rp=%s)", cfg.Auth.WebAuthn.RPID)
		wa.Routes(rt)
	}

	sessions.Routes(rt)
	// API keys for scripts and integrations: managed with a session in
	// Settings, accepted on RPCs alongside session tokens.
	keys := auth.NewAPIKeys(store.DB(), sessions, store.Users(), store.Members())
	keys.Routes(rt)
	// Demo sessions and read-scoped API keys are read-only: reject mutating
	// RPCs server-side.
	return []connect.ServerInterceptor{auth.SessionInterceptor(keys), auth.ReadOnlyGuard()}
}

// maxRPCMessage caps one RPC message. A Push after a long offline period is
// the largest legitimate request; clients split bigger batches.
const maxRPCMessage = 16 << 20

// mountRPC serves the registered Connect services with the message cap.
func mountRPC(mux *http.ServeMux, server *connect.Server) {
	connecthttp.Mount(mux, server, connecthttp.WithReadMaxBytes(maxRPCMessage))
}

// registerServices mounts the Connect-RPC handlers, blob serving and health.
func registerServices(mux *http.ServeMux, cfg *config.Config, store *sqlstore.Store, interceptors []connect.ServerInterceptor) {
	server := connect.NewServer(interceptors...)

	// One clock for every server-side write, shared with the sync endpoint.
	hlc := wsync.NewHLC(cfg.Sync.NodeID)
	// Offline sync: central push/pull endpoint (the app's write path).
	syncSvc := service.NewSyncService(store.DB(), hlc)
	syncSvc.MinProtocol = cfg.Sync.MinProtocol
	openbeehivev1connect.RegisterSyncServiceHandler(server, syncSvc)
	// CRUD services for scripts and integrations. Their writes go through the
	// same merge + change log as Push, so they show up on devices.
	w := service.NewWriter(store.DB(), hlc)
	openbeehivev1connect.RegisterApiaryServiceHandler(server, service.NewApiaryService(store.Apiaries(), w))
	openbeehivev1connect.RegisterHiveServiceHandler(server, service.NewHiveService(store.Hives(), w))
	openbeehivev1connect.RegisterQueenServiceHandler(server, service.NewQueenService(w))
	openbeehivev1connect.RegisterInspectionServiceHandler(server, service.NewInspectionService(w))
	openbeehivev1connect.RegisterTaskServiceHandler(server, service.NewTaskService(w))
	openbeehivev1connect.RegisterTreatmentServiceHandler(server, service.NewTreatmentService(w))
	openbeehivev1connect.RegisterHarvestServiceHandler(server, service.NewHarvestService(w))
	openbeehivev1connect.RegisterEventServiceHandler(server, service.NewEventService(w))
	openbeehivev1connect.RegisterStatsServiceHandler(server, service.NewStatsService(w))
	mountRPC(mux, server)

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
		log.Printf("web: not serving embedded app (%v); run vite separately in dev", err)
		return
	}
	mux.Handle("/", h)
	log.Printf("web: serving embedded app at /")
}

// serve runs the HTTP server until SIGINT/SIGTERM, then shuts down gracefully.
func serve(ctx context.Context, cfg *config.Config, handler http.Handler) {
	// Unencrypted HTTP/2 (h2c) allows gRPC without TLS; TLS terminates at the reverse proxy.
	// The read and write timeouts bound every request; web.NoDeadline lifts
	// them for streaming RPCs.
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
	log.Printf("shutting down...")
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
