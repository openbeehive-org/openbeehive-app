package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	v1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1/openbeehivev1connect"
)

type tokenVerifier string

func (t tokenVerifier) Verify(_ context.Context, token string) (Identity, error) {
	if token != string(t) {
		return Identity{}, errors.New("bad token")
	}
	return Identity{UserID: "u1", OrgID: "o1"}, nil
}

// whoami answers Pull with the caller's user ID as cursor.
type whoami struct {
	openbeehivev1connect.UnimplementedSyncServiceHandler
}

func (whoami) Pull(ctx context.Context, _ *v1.PullRequest) (*v1.PullResponse, error) {
	id, _ := FromContext(ctx)
	return &v1.PullResponse{NextCursor: id.UserID}, nil
}

func TestSessionInterceptorOverHTTP(t *testing.T) {
	server := connect.NewServer(SessionInterceptor(tokenVerifier("good")))
	openbeehivev1connect.RegisterSyncServiceHandler(server, whoami{})
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	client := openbeehivev1connect.NewSyncServiceClient(connect.NewClient(
		connecthttp.NewTransport(ts.Client(), ts.URL)))

	for _, tc := range []struct {
		name, header, value string
		want                connect.Code
	}{
		{"no token", "", "", connect.CodeUnauthenticated},
		{"bad bearer", "Authorization", "Bearer nope", connect.CodeUnauthenticated},
		{"bearer", "Authorization", "Bearer good", 0},
		{"cookie", "Cookie", "other=x; " + SessionCookie + "=good", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, info := connect.NewClientContext(t.Context())
			if tc.header != "" {
				info.RequestHeader().Set(tc.header, tc.value)
			}
			res, err := client.Pull(ctx, &v1.PullRequest{})
			if tc.want != 0 {
				if connect.CodeOf(err) != tc.want {
					t.Fatalf("want %v, got %v", tc.want, err)
				}
				return
			}
			if err != nil || res.GetNextCursor() != "u1" {
				t.Fatalf("want identity u1, got %v / %v", res, err)
			}
		})
	}
}
