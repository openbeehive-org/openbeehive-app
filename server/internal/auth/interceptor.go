package auth

import (
	"context"
	"errors"

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
// account and read-scoped API keys. RPCs classified as reads pass, including
// the Subscribe stream.
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

// rpcAccess says whether an RPC only reads or also changes data.
type rpcAccess int

const (
	rpcRead rpcAccess = iota + 1
	rpcWrite
)

// rpcAccessByProcedure classifies every RPC of the API. A procedure that is
// missing here counts as a write, so a new RPC is closed to read-only
// identities until someone decides otherwise; TestEveryRPCIsClassified fails
// until it is listed.
var rpcAccessByProcedure = map[string]rpcAccess{
	"/openbeehive.v1.SyncService/Pull":      rpcRead,
	"/openbeehive.v1.SyncService/Push":      rpcWrite,
	"/openbeehive.v1.SyncService/Subscribe": rpcRead,

	"/openbeehive.v1.ApiaryService/CreateApiary": rpcWrite,
	"/openbeehive.v1.ApiaryService/GetApiary":    rpcRead,
	"/openbeehive.v1.ApiaryService/ListApiaries": rpcRead,
	"/openbeehive.v1.ApiaryService/UpdateApiary": rpcWrite,
	"/openbeehive.v1.ApiaryService/DeleteApiary": rpcWrite,

	"/openbeehive.v1.HiveService/CreateHive":   rpcWrite,
	"/openbeehive.v1.HiveService/GetHive":      rpcRead,
	"/openbeehive.v1.HiveService/ListHives":    rpcRead,
	"/openbeehive.v1.HiveService/UpdateHive":   rpcWrite,
	"/openbeehive.v1.HiveService/DeleteHive":   rpcWrite,
	"/openbeehive.v1.HiveService/RelocateHive": rpcWrite,

	"/openbeehive.v1.QueenService/CreateQueen": rpcWrite,
	"/openbeehive.v1.QueenService/ListQueens":  rpcRead,
	"/openbeehive.v1.QueenService/UpdateQueen": rpcWrite,
	"/openbeehive.v1.QueenService/DeleteQueen": rpcWrite,

	"/openbeehive.v1.InspectionService/CreateInspection":      rpcWrite,
	"/openbeehive.v1.InspectionService/ListInspections":       rpcRead,
	"/openbeehive.v1.InspectionService/DeleteInspection":      rpcWrite,
	"/openbeehive.v1.InspectionService/UpdateInspection":      rpcWrite,
	"/openbeehive.v1.InspectionService/AddInspectionPhoto":    rpcWrite,
	"/openbeehive.v1.InspectionService/RemoveInspectionPhoto": rpcWrite,

	"/openbeehive.v1.TaskService/CreateTask":  rpcWrite,
	"/openbeehive.v1.TaskService/ListTasks":   rpcRead,
	"/openbeehive.v1.TaskService/SetTaskDone": rpcWrite,
	"/openbeehive.v1.TaskService/UpdateTask":  rpcWrite,
	"/openbeehive.v1.TaskService/DeleteTask":  rpcWrite,

	"/openbeehive.v1.TreatmentService/CreateTreatment": rpcWrite,
	"/openbeehive.v1.TreatmentService/ListTreatments":  rpcRead,
	"/openbeehive.v1.TreatmentService/DeleteTreatment": rpcWrite,
	"/openbeehive.v1.TreatmentService/UpdateTreatment": rpcWrite,

	"/openbeehive.v1.HarvestService/CreateHarvest": rpcWrite,
	"/openbeehive.v1.HarvestService/ListHarvests":  rpcRead,
	"/openbeehive.v1.HarvestService/DeleteHarvest": rpcWrite,
	"/openbeehive.v1.HarvestService/UpdateHarvest": rpcWrite,

	"/openbeehive.v1.EventService/ListEvents": rpcRead,

	"/openbeehive.v1.StatsService/GetDashboard":  rpcRead,
	"/openbeehive.v1.StatsService/GetHoneyStats": rpcRead,
}

// readOnlyMethod reports whether the procedure (for example
// "/openbeehive.v1.SyncService/Pull") is classified as a read.
func readOnlyMethod(procedure string) bool {
	return rpcAccessByProcedure[procedure] == rpcRead
}
