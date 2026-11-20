package auth

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	_ "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1" // registers the API's descriptors
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

// Every RPC in the proto contract must be classified as read or write. An
// unclassified RPC is treated as a write, which is safe but locks read-only
// keys and the demo out of a new reading RPC, so the omission has to be a
// decision, not an accident.
func TestEveryRPCIsClassified(t *testing.T) {
	defined := map[string]bool{}
	protoregistry.GlobalFiles.RangeFilesByPackage("openbeehive.v1", func(fd protoreflect.FileDescriptor) bool {
		services := fd.Services()
		for i := range services.Len() {
			svc := services.Get(i)
			methods := svc.Methods()
			for j := range methods.Len() {
				defined["/"+string(svc.FullName())+"/"+string(methods.Get(j).Name())] = true
			}
		}
		return true
	})
	if len(defined) == 0 {
		t.Fatal("no RPCs found in the proto registry")
	}
	for proc := range defined {
		if access, ok := rpcAccessByProcedure[proc]; !ok || (access != rpcRead && access != rpcWrite) {
			t.Errorf("%s is not classified: add it to rpcAccessByProcedure as rpcRead or rpcWrite", proc)
		}
	}
	for proc := range rpcAccessByProcedure {
		if !defined[proc] {
			t.Errorf("%s is classified but no longer exists in the proto contract", proc)
		}
	}
}

func TestUnclassifiedRPCCountsAsWrite(t *testing.T) {
	ctx := WithIdentity(context.Background(), Identity{UserID: "u", OrgID: "o", ReadOnly: true})
	for _, proc := range []string{
		"/openbeehive.v1.ApiaryService/GetEverything", // looks like a read by name
		"/openbeehive.v1.NewService/ListThings",
		"",
	} {
		if err := callGuard(ctx, proc); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("%q: an unclassified RPC must be closed to read-only identities, got %v", proc, err)
		}
	}
}
