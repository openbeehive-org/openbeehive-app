package auth

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect/v2"
)

type ctxKey int

const identityKey ctxKey = 0

// Identity is attached to the request context after successful auth.
type Identity struct {
	UserID string
	OrgID  string // active tenant
	Email  string
	Role   string // account role: admin | user | demo
	// ReadOnly marks identities that may only call reading RPCs: the demo
	// account and API keys created with the "read" scope.
	ReadOnly bool
}

func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey, id)
}

// FromContext returns the identity; ok=false if not authenticated.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey).(Identity)
	return id, ok
}

// SessionVerifier is implemented by the session manager (app session token).
// The Connect interceptor (SessionInterceptor) uses it to authenticate requests.
type SessionVerifier interface {
	Verify(ctx context.Context, token string) (Identity, error)
}

var errMissingToken = errors.New("missing session token")

// ErrDemoReadOnly rejects writes from demo sessions.
var ErrDemoReadOnly = errors.New("the demo account is read-only; data resets hourly")

// ErrKeyReadOnly rejects writes from read-scoped API keys.
var ErrKeyReadOnly = errors.New("this API key is read-only")

// ReadOnlyGuard rejects mutating RPCs for read-only identities: the demo
// account and read-scoped API keys. Read methods pass; handler streams
// (Subscribe) are read-only by design and pass unchanged.
func ReadOnlyGuard() connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			id, ok := FromContext(ctx)
			if !ok || readOnlyMethod(spec.Procedure) {
				return next(ctx, spec, stream)
			}
			if id.Role == "demo" {
				return connect.NewError(connect.CodePermissionDenied, ErrDemoReadOnly.Error()).WithCause(ErrDemoReadOnly)
			}
			if id.ReadOnly {
				return connect.NewError(connect.CodePermissionDenied, ErrKeyReadOnly.Error()).WithCause(ErrKeyReadOnly)
			}
			return next(ctx, spec, stream)
		}
	}
}

// readOnlyMethod allow-lists reading RPC name prefixes; everything else counts
// as a write. procedure looks like "/openbeehive.v1.SyncService/Pull".
func readOnlyMethod(procedure string) bool {
	method := procedure
	if i := strings.LastIndexByte(procedure, '/'); i >= 0 {
		method = procedure[i+1:]
	}
	for _, p := range []string{"Get", "List", "Pull", "Subscribe"} {
		if strings.HasPrefix(method, p) {
			return true
		}
	}
	return false
}
