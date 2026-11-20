package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	v1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1/openbeehivev1connect"
)

type acceptingSync struct {
	openbeehivev1connect.UnimplementedSyncServiceHandler
}

func (acceptingSync) Push(context.Context, *v1.PushRequest) (*v1.PushResponse, error) {
	return &v1.PushResponse{ServerCursor: "1"}, nil
}

// One RPC message is capped at 16 MiB: enough for a long-offline Push, not
// enough to make the server buffer whatever a client sends.
func TestRPCMessagesAreCapped(t *testing.T) {
	server := connect.NewServer()
	openbeehivev1connect.RegisterSyncServiceHandler(server, acceptingSync{})
	mux := http.NewServeMux()
	mountRPC(mux, server)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	client := openbeehivev1connect.NewSyncServiceClient(connect.NewClient(connecthttp.NewTransport(ts.Client(), ts.URL)))

	push := func(bytes int) error {
		_, err := client.Push(t.Context(), &v1.PushRequest{Changes: []*v1.Change{{
			Entity: "apiary", EntityId: "a", PayloadJson: strings.Repeat("x", bytes),
		}}})
		return err
	}
	if err := push(maxRPCMessage - 1024); err != nil {
		t.Fatalf("a push just under the cap must pass: %v", err)
	}
	if err := push(maxRPCMessage + 1); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("a push over the cap: want ResourceExhausted, got %v", err)
	}
}

func TestStreamingProceduresAreFoundInTheContract(t *testing.T) {
	got := streamingProcedures()
	if len(got) != 1 || !got[openbeehivev1connect.SyncServiceSubscribeProcedure] {
		t.Fatalf("streaming procedures: %v, want only Subscribe", got)
	}
}

func request(h http.Handler, method, host, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://placeholder/healthz", nil)
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
		if method == http.MethodOptions {
			req.Header.Set("Access-Control-Request-Method", "POST")
			req.Header.Set("Access-Control-Request-Headers", "content-type")
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func healthMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	return mux
}

// Without login every request has full access, so the server must not answer
// to a host name a foreign web page picked, nor grant other origins access.
func TestNoLoginModeIsClosedToOtherSites(t *testing.T) {
	cfg := &config.Config{PublicBaseURL: "http://localhost:8080"}
	h := harden(cfg, healthMux())

	if rec := request(h, http.MethodGet, "localhost:8080", ""); rec.Code != http.StatusOK {
		t.Fatalf("localhost: %d", rec.Code)
	}
	if rec := request(h, http.MethodGet, "192.168.1.20:8080", ""); rec.Code != http.StatusOK {
		t.Fatalf("LAN address: %d", rec.Code)
	}
	rec := request(h, http.MethodGet, "rebind.evil.example:8080", "")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "BEEHIVE_ALLOWED_HOSTS") {
		t.Fatalf("foreign host name: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Content-Security-Policy") == "" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("rejections carry the security headers too: %v", rec.Header())
	}

	// Same-origin default: another origin gets no CORS grant, not even on preflight.
	for _, method := range []string{http.MethodGet, http.MethodOptions} {
		rec := request(h, method, "localhost:8080", "https://evil.example")
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("%s: default configuration granted CORS to %q", method, got)
		}
		if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
			t.Fatalf("%s: default configuration allowed credentials", method)
		}
	}

	cfg.AllowedHosts = []string{"bees.lan"}
	if rec := request(harden(cfg, healthMux()), http.MethodGet, "bees.lan", ""); rec.Code != http.StatusOK {
		t.Fatalf("allowed host: %d", rec.Code)
	}
}

func TestLoginModeHasNoHostCheck(t *testing.T) {
	cfg := &config.Config{PublicBaseURL: "https://bees.example.org"}
	cfg.Auth.PasswordEnabled = true
	if rec := request(harden(cfg, healthMux()), http.MethodGet, "anything.example", ""); rec.Code != http.StatusOK {
		t.Fatalf("with login the Host is not restricted: %d", rec.Code)
	}
}

func TestCORSOnlyForConfiguredOrigins(t *testing.T) {
	cfg := &config.Config{PublicBaseURL: "https://bees.example.org"}
	cfg.Auth.PasswordEnabled = true
	cfg.CORS = config.CORSConfig{AllowedOrigins: []string{"https://app.example.org"}, AllowCredentials: true}
	h := harden(cfg, healthMux())
	rec := request(h, http.MethodGet, "api.example.org", "https://app.example.org")
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://app.example.org" ||
		rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("configured origin: %v", rec.Header())
	}
	if rec := request(h, http.MethodGet, "api.example.org", "https://evil.example"); rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("unlisted origin was granted: %v", rec.Header())
	}

	// Any origin: allowed, but never with credentials (config.Load turns
	// them off; see TestLoadCORSDefaultsToSameOrigin).
	cfg.CORS = config.CORSConfig{AllowedOrigins: []string{"*"}, AllowCredentials: false}
	rec = request(harden(cfg, healthMux()), http.MethodGet, "api.example.org", "https://anything.example")
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" || rec.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("wildcard origin: %v", rec.Header())
	}
}
