package auth

import (
	"context"

	"connectrpc.com/connect/v2"
)

// LocalUser is the fixed identity used in self-host single-user mode, i.e.
// when no auth method (OIDC, WebAuthn, password) is configured. It lets the offline-first app work
// out of the box without a login flow: one beekeeper, one tenant.
var LocalUser = Identity{
	UserID: "local",
	OrgID:  "local",
	Email:  "local@openbeehive",
	Role:   "owner",
}

// DevInterceptor injects LocalUser into every RPC context. It is wired in
// only when authentication is disabled (selfhost without OIDC). Never enable
// it together with the real session interceptor.
func DevInterceptor() connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			return next(WithIdentity(ctx, LocalUser), spec, stream)
		}
	}
}
