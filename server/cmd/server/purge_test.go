package main

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	v1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1/openbeehivev1connect"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage/blob"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// Deleting a workspace removes its blobs and nobody else's, and the log
// names no image data.
func TestDeletingAWorkspacePurgesItsBlobs(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	w := newWired(t, loginConfig())
	owner, neighbour := w.signup("owner@example.org"), w.signup("neighbour@example.org")
	full := testJPEG(t, 64, 64)
	for i, m := range []member{owner, owner, neighbour} {
		id := w.newPhoto(m, "photo-"+string(rune('a'+i)))
		if res := w.do(http.MethodPost, "/photos/"+id, full, "Authorization", "Bearer "+m.token); res.status != http.StatusCreated {
			t.Fatalf("upload: %d %s", res.status, res.body)
		}
	}
	// Also an inline photo of a 0.4.x app, which used to end up in the log.
	inline := "data:image/jpeg;base64," + strings.Repeat("QUJD", 2000)
	if _, err := openbeehivev1connect.NewSyncServiceClient(w.rpc(owner)).Push(context.Background(), &v1.PushRequest{Changes: []*v1.Change{{
		Entity: "apiary", EntityId: "apiary-x", ScopeId: "apiary-x", Op: v1.ChangeOp_CHANGE_OP_UPSERT,
		PayloadJson: `{"name":"X","created_at":"2026-01-01","updated_at":"2026-01-01"}`, Hlc: wsync.NewHLC("device").Now(),
	}, {
		Entity: "hive", EntityId: "hive-x", ScopeId: "apiary-x", Op: v1.ChangeOp_CHANGE_OP_UPSERT,
		PayloadJson: `{"apiary_id":"apiary-x","name":"H","created_at":"2026-01-01","updated_at":"2026-01-01","photo":"` + inline + `"}`,
		Hlc:         wsync.NewHLC("device2").Now(),
	}}}); err != nil {
		t.Fatal(err)
	}

	w.postJSON("/tenants/delete", owner.token, map[string]string{"org_id": owner.org})
	deadline := time.Now().Add(10 * time.Second)
	for {
		keys := w.keys()
		if len(keys) == 1 && strings.HasPrefix(keys[0], blob.WorkspacePrefix(neighbour.org)) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("blobs after deleting the workspace: %v, want only the neighbour's", keys)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Give the purge a moment to write its log line, then read the log.
	time.Sleep(100 * time.Millisecond)
	if out := logged.String(); strings.Contains(out, "data:image") || strings.Contains(out, "QUJDQUJD") || !strings.Contains(out, "purged 2 photo blobs") {
		t.Fatalf("log after the purge: %.300s", out)
	}
}
