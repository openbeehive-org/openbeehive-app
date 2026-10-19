package service

import (
	"testing"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// Fixed device stamps; the change HLC doubles as the OR-Set add tag.
const (
	tagA = "000000000001000:00000:devA"
	tagB = "000000000002000:00000:devB"
	tagC = "000000000003000:00000:devA"
	tagD = "000000000004000:00000:devB"
)

func orsetChange(id, payload, hlc string) *wv1.Change {
	return &wv1.Change{
		Entity: "inspection", EntityId: id, ScopeId: "apiary-a",
		Op: wv1.ChangeOp_CHANGE_OP_UPSERT, PayloadJson: payload,
		Hlc: hlc, AuthorId: "dev",
	}
}

func photoSet(t *testing.T, svc *SyncService, id string) []string {
	t.Helper()
	var raw string
	if err := svc.db.Get(&raw, svc.db.Rebind(`SELECT photo_keys FROM inspection WHERE id = ?`), id); err != nil {
		t.Fatalf("read photo_keys: %v", err)
	}
	return wsync.ParseORSet(raw).Values()
}

// TestPushORSetRemoveCarriesObservedTags: device A removes a photo it has
// seen while device B concurrently re-added it. A's remove lists only the
// tags A observed, so B's add must survive on the server (add-wins).
func TestPushORSetRemoveCarriesObservedTags(t *testing.T) {
	svc := newSyncFixture(t)
	ctx := identityCtx("user-a", "org-a")

	// A creates the inspection with photo p1 (tag = A's change HLC).
	if err := push(svc, ctx, orsetChange("insp-1",
		`{"hive_id":"hive-a","date":"2026-07-11T10:00:00Z","created_at":"2026-07-11T10:00:00Z","deleted":0,"photo_keys":{"add":["p1"]}}`, tagA)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// B re-adds p1 concurrently (tag unobserved by A).
	if err := push(svc, ctx, orsetChange("insp-1",
		`{"photo_keys":{"add":["p1"]}}`, tagB)); err != nil {
		t.Fatalf("concurrent add: %v", err)
	}
	// A removes p1, listing only its own observed tag.
	if err := push(svc, ctx, orsetChange("insp-1",
		`{"photo_keys":{"remove":["p1"],"removed_tags":{"p1":["`+tagA+`"]}}}`, tagC)); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if got := photoSet(t, svc, "insp-1"); len(got) != 1 || got[0] != "p1" {
		t.Fatalf("add-wins violated: B's unobserved add must survive, got %v", got)
	}

	// Once B's tag is also tombstoned, the element disappears.
	if err := push(svc, ctx, orsetChange("insp-1",
		`{"photo_keys":{"remove":["p1"],"removed_tags":{"p1":["`+tagB+`"]}}}`, tagD)); err != nil {
		t.Fatalf("second remove: %v", err)
	}
	if got := photoSet(t, svc, "insp-1"); len(got) != 0 {
		t.Fatalf("all tags tombstoned, element must vanish: %v", got)
	}
}

// TestPushORSetLegacyRemove: deltas from older app versions carry no
// removed_tags — the server falls back to removing every tag it currently
// observes (pre-existing behavior, kept for compatibility).
func TestPushORSetLegacyRemove(t *testing.T) {
	svc := newSyncFixture(t)
	ctx := identityCtx("user-a", "org-a")

	if err := push(svc, ctx, orsetChange("insp-2",
		`{"hive_id":"hive-a","date":"2026-07-11T10:00:00Z","created_at":"2026-07-11T10:00:00Z","deleted":0,"photo_keys":{"add":["p1"]}}`, tagA)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := push(svc, ctx, orsetChange("insp-2",
		`{"photo_keys":{"remove":["p1"]}}`, tagC)); err != nil {
		t.Fatalf("legacy remove: %v", err)
	}
	if got := photoSet(t, svc, "insp-2"); len(got) != 0 {
		t.Fatalf("legacy remove must clear all observed tags: %v", got)
	}
}
