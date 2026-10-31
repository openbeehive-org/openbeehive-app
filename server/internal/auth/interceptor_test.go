package auth

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect/v2"
)

func callGuard(ctx context.Context, procedure string) error {
	next := func(context.Context, connect.Spec, connect.ServerStream) error { return nil }
	return ReadOnlyGuard()(next)(ctx, connect.Spec{Procedure: procedure}, nil)
}

func TestReadOnlyGuardRejectsDemoWrites(t *testing.T) {
	ctx := WithIdentity(context.Background(), Identity{UserID: "u", OrgID: "o", Role: "demo"})
	for _, proc := range []string{
		"/openbeehive.v1.SyncService/Push",
		"/openbeehive.v1.ApiaryService/CreateApiary",
		"/openbeehive.v1.ApiaryService/DeleteApiary",
	} {
		err := callGuard(ctx, proc)
		var cerr *connect.Error
		if !errors.As(err, &cerr) || cerr.Code() != connect.CodePermissionDenied {
			t.Fatalf("%s: want PermissionDenied for demo, got %v", proc, err)
		}
	}
}

func TestReadOnlyGuardAllowsDemoReads(t *testing.T) {
	ctx := WithIdentity(context.Background(), Identity{UserID: "u", OrgID: "o", Role: "demo"})
	for _, proc := range []string{
		"/openbeehive.v1.SyncService/Pull",
		"/openbeehive.v1.ApiaryService/ListApiaries",
		"/openbeehive.v1.ApiaryService/GetApiary",
		"/openbeehive.v1.SyncService/Subscribe",
		"/openbeehive.v1.InspectionService/ListInspections",
		"/openbeehive.v1.StatsService/GetDashboard",
		"/openbeehive.v1.StatsService/GetHoneyStats",
	} {
		if err := callGuard(ctx, proc); err != nil {
			t.Fatalf("%s: demo read must pass, got %v", proc, err)
		}
	}
}

func TestReadOnlyGuardAllowsNormalUsers(t *testing.T) {
	for _, role := range []string{"admin", "user", ""} {
		ctx := WithIdentity(context.Background(), Identity{UserID: "u", OrgID: "o", Role: role})
		if err := callGuard(ctx, "/openbeehive.v1.SyncService/Push"); err != nil {
			t.Fatalf("role %q: write must pass, got %v", role, err)
		}
	}
}

func TestReadOnlyGuardUnauthenticatedPassesThrough(t *testing.T) {
	// No identity in context: the guard is not the auth layer; it must defer.
	if err := callGuard(context.Background(), "/openbeehive.v1.SyncService/Push"); err != nil {
		t.Fatalf("unauthenticated pass-through: %v", err)
	}
}
