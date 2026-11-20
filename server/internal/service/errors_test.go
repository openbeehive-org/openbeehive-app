package service

import (
	"testing"

	"connectrpc.com/connect/v2"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
)

func TestDatabaseErrorsAreNotShownToClients(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	// Break the schema so every statement on these tables fails in the driver.
	for _, table := range []string{"inspection", "apiary", "hive"} {
		f.db.MustExec("DROP TABLE " + table)
	}
	_, listErr := f.insp.ListInspections(ctx, &wv1.ListInspectionsRequest{})
	_, apiaryErr := f.apiary.ListApiaries(ctx, &wv1.ListApiariesRequest{})
	_, hiveErr := f.hive.ListHives(ctx, &wv1.ListHivesRequest{})
	_, getErr := f.apiary.GetApiary(ctx, &wv1.GetApiaryRequest{Id: "apiary-a"})
	_, writeErr := f.apiary.CreateApiary(ctx, &wv1.CreateApiaryRequest{Name: "x"})
	for name, err := range map[string]error{"list": listErr, "apiaries": apiaryErr, "hives": hiveErr, "get": getErr, "write": writeErr} {
		wantCode(t, err, connect.CodeInternal)
		if msg := err.Error(); msg != "internal: internal error" {
			t.Fatalf("%s: the driver's message must stay in the log, got %q", name, msg)
		}
	}
}
