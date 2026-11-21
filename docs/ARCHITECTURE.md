# Architecture

Openbeehive is a local-first beekeeping app: a SvelteKit PWA that keeps its own SQLite database on the
device, and a Go server that orders and distributes changes between devices. This document describes the
components in this repository, their boundaries and how data flows between them. The user-facing
explanation lives in the docs site (<https://docs.openbeehive.org/developers/architecture>); the decisions
behind the structure are recorded in `docs/adr/`.

## Components

```text
proto/      Connect-RPC contract (.proto): the source of truth for the API
server/     Go backend
  cmd/server          wiring: config, storage, auth, RPC handlers, embedded SPA, HTTP server
  internal/config     every runtime value, read from BEEHIVE_* environment variables
  internal/auth       sessions, email+password, OIDC, WebAuthn, API keys, tenants, read-only guard
  internal/service    Connect-RPC handlers: sync (push/pull/snapshot/subscribe), CRUD per entity, stats
  internal/sync       hybrid logical clock, per-field clocks, OR-Set merge
  internal/storage    pluggable SQL (Postgres, MySQL, SQLite) and blob (MinIO/S3, filesystem) backends
  internal/web        the embedded SPA build (single-binary production)
  internal/gen        generated stubs (not committed; `make proto`)
app/        SvelteKit PWA
  src/lib/local       SQLite-WASM on OPFS, repository layer, HLC, merge, sync engine
  src/lib/data        import/export (CSV, XLSX, BeeXML, ZIP)
  src/lib/proto       generated TypeScript stubs (not committed; `make proto`)
  src/routes          screens
```

## Boundaries

- **The proto contract is the API.** `proto/openbeehive/v1` defines every message and service. Go and
  TypeScript stubs are generated from it (`buf generate`, remote plugins pinned in `buf.gen.yaml`) and
  never edited or committed. `make proto-breaking` guards the contract against breaking changes.
- **The app only talks to its local database.** Screens read and write SQLite-WASM through
  `src/lib/local/repo.ts`; nothing on the interaction path touches the network. The sync engine
  (`src/lib/local/sync.ts`) pushes and pulls in the background.
- **The server orders changes, it does not own the UI.** `SyncService.Push` merges each change per field
  (last-writer-wins by HLC for scalars, add-wins OR-Set for list fields) and appends it to the
  `change_log`; `Pull` serves the log from a cursor, limited to the caller's workspace (personal rows
  only to their owner); `Snapshot` serves the current rows to a device that cannot or need not replay
  the log.
- **CRUD services are a second door to the same room.** Scripts and devices use `ApiaryService`,
  `HiveService`, … with API keys. Their writes go through the same `Writer` (merge + change log) as a
  pushed change, so devices see them on the next pull.
- **Storage is pluggable behind two interfaces.** `storage/sql.Store` (sqlx, portable SQL with `?`
  placeholders and `Rebind`) and `storage/blob.Store`. The deployment profile picks the defaults
  (`selfhost`: SQLite + filesystem, `cloud`: Postgres + MinIO); `BEEHIVE_*` variables override them.
- **Auth is middleware.** Session tokens (cookie or bearer) and API keys resolve to an `Identity`
  (user, organization, role) in a Connect interceptor; a read-only guard rejects mutating RPCs for demo
  sessions and read-scoped keys. Without any login method configured the server injects a fixed local
  identity (single-user self-hosting).

## Data flow

1. A tap in the app writes a row locally and records a `Change` (entity, id, op, payload, HLC) in the
   outbox.
2. The sync engine pushes outbox changes to `SyncService.Push`. The server applies each change on its
   own: it checks workspace, scope, stamp and payload, merges the known columns field by field against
   the stored row's field clocks, and appends the normalised change to the log. A change it refuses is
   reported back with a code and does not hold up the others.
3. Other devices call `Pull` with their cursor and receive the new log entries of their workspace,
   apply them with the same merge rules, and advance the cursor (only `Pull` and `Snapshot` set it).
   `Subscribe` nudges connected devices when their workspace has news.
4. Any two devices that have seen the same changes converge to the same state, regardless of arrival
   order (the merge is deterministic; `server/internal/sync` and `app/src/lib/local/merge.ts` mirror
   each other, run the same cases from `server/internal/sync/testdata/merge_vectors.json`, and must
   change together).

## Sync protocol

`proto/openbeehive/v1/sync.proto` is the reference; this is how the parts fit together.

- **Versions.** An app says which protocol version it speaks in the `Openbeehive-Sync-Protocol`
  request header (and its release in `Openbeehive-App-Version`). The current version is 1. A request
  without the header comes from an installed 0.4.x app. `BEEHIVE_SYNC_MIN_PROTOCOL` (default 0) is the
  lowest version the server accepts; below it every sync call fails with `FAILED_PRECONDITION` and an
  `UpgradeRequired` detail, which a current app shows as "update the app" instead of retrying.
- **Old apps.** A 0.4.x app writes every key of a pulled payload into a column of that name and
  stops syncing for good at the first key it has no column for. For clients without a protocol
  version, `Pull` therefore returns only the tables and columns of release 0.4.1 (`legacyCols` in
  `server/internal/service/sync_legacy.go`, a list that must never grow) and plain sequence numbers
  as cursors. New synced columns and tables reach only clients that announce a version.
  `TestLegacyPullStaysInsideTheFrozenSet` (Go) and `app/src/lib/local/legacy.test.ts`, which runs the
  pull code of release 0.4.1 on what the server sends, guard this.
- **Cursor and epoch.** Every database has a random epoch (`sync_state`), and every start of the
  server leaves a mark at the log position it started at (`sync_boot`). For protocol 1 a cursor is
  `<epoch>:<sequence number>:<mark of the last start before that number>`. `Pull` answers a cursor
  that is not part of this database's history with `RESET_REQUIRED` instead of changes: another
  epoch, a number beyond the end of the log, or a mark that is not the one that belongs there. The
  last two are what a restored backup looks like: the server starts on it at an earlier position
  than devices have read, and hands the numbers after it out again. The check does not depend on
  log entries, so compaction, a deleted workspace or the rebuilt demo cannot hide a restore. It does
  depend on the server being started on the restored database before it serves requests.
- **Snapshot.** `Snapshot` returns the current rows of the workspace (tombstones included, personal
  rows only to their owner) with their field clocks, in pages cut by size, plus the cursor to
  continue from. The device merges each row column by column, so newer local edits survive and the
  outbox is untouched. A device uses it when it has no cursor, after `RESET_REQUIRED`, and after
  `SNAPSHOT_REQUIRED`.
- **Reset.** After `RESET_REQUIRED` the device also queues every local row for upload again, as
  ordinary changes grouped by the stamps the columns already carry; the oldest change of a row
  carries all its columns, so it can create a row the server no longer has. Where the server still
  has a change the merge does nothing; where the restore lost one, it comes back. 0.4.x apps get neither
  signal; one whose position lies beyond the restored log reads the log again from the start.
- **Compaction.** Once a day the server deletes log entries it received more than
  `BEEHIVE_CHANGELOG_RETENTION_DAYS` ago (default 180, 0 keeps everything) and stores the sequence
  number up to which it did (the horizon). A cursor older than the horizon gets
  `SNAPSHOT_REQUIRED`; a 0.4.x app gets the `UpgradeRequired` failure. A device that keeps syncing
  moves its cursor along with the log even when its workspace is quiet. Nothing else reads old log
  entries: who owns a personal task, for example, is stored on the task (`task.owner_id`). The job
  runs in the server process and assumes it is the only one: run exactly one instance per database.
- **Account settings.** `user_settings` (units, language) is the one synced table whose rows belong
  to an account instead of a workspace: one row per account, id = the account id, written only in
  the account's personal scope. Its log entries are filed under the account (`org_id`
  `user:<id>`), so `Pull`, `Subscribe` and `Snapshot` serve it in every workspace of the account and
  each workspace's local database holds a copy of the same row. Leaving a workspace keeps it;
  deleting the account removes it. Values are display preferences only: records, payloads, exports
  and the API stay metric. On the device `src/lib/prefs.ts` keeps what was last applied (so the
  app starts with it before a database opens) and `src/lib/local/userSettings.ts` reconciles it
  with the record by field stamp. The colour theme is not part of it; it stays per device.

## Photos

A photo is two things. Its record (`photo`) syncs like every other row and carries a thumbnail of a
few KB, so every device shows the photo at once and offline. Its full-size image (at most 2048 px and
5 MiB) lives in the blob store under `<workspace>/photos/<hash of the photo id>` and never enters the
sync feed.

- The device that takes a photo stores the image locally first (IndexedDB, `app/src/lib/photos`),
  then writes the record, and uploads the image to `POST /photos/<id>` whenever it is online. Other
  devices fetch it with `GET /photos/<id>` the first time the photo is opened and keep a copy. Both
  endpoints need the caller's session or API key and membership of the photo's workspace; the blob
  store itself is never exposed.
- Deleting a photo is final: the server erases the thumbnail from the row and from the change log
  and removes the blob (`server/internal/service/photo_sync.go`, `photo_sweep.go`).
- Apps up to 0.4.x store photos inline (data URLs in `inspection.photo_keys` and `hive.photo`). The
  server keeps accepting and serving that format for them and converts it to photo records
  (`photo_legacy.go`; existing data once at start-up, `photo_boot.go`). Clients that announce a
  protocol version get the photo records and never the inline copies (`photo_feed.go`); 0.4.x apps
  get the inline copies and never the `photo` entity. Removes of inline photos stay in the log for
  90 days with their data URL, so 0.4.x devices that were offline learn of the delete.
- A snapshot carries the photo records with their thumbnails, never the images. After
  `RESET_REQUIRED` a device queues every image it still keeps for upload again, after the records.

A backup is the database plus the blob directory or bucket.

### Removing the 0.4.x compatibility (planned for 0.6)

0.5.x serves 0.4.x apps (decision D3). In 0.6, `BEEHIVE_SYNC_MIN_PROTOCOL` defaults to 1, so apps
that announce no protocol version get the `UpgradeRequired` failure, and the code that exists only for
them goes:

- the legacy feed: `legacyCols` and the reduced pull in `server/internal/service/sync_legacy.go`,
  `syncClient.legacy()` and its callers (`sync_client.go`, `sync.go`, `sync_snapshot.go`), any
  special handling of pushes from unversioned apps, and their tests (`sync_legacy_test.go`,
  `app/src/lib/local/legacy.test.ts`);
- the push-side conversion of inline photos in both directions: `photo_legacy.go` (inline adds and
  removes on `inspection.photo_keys` / `hive.photo` to photo records, and photo records back to inline
  copies), the inline filtering in `photo_feed.go`, the inline branches of `photo_sync.go`
  (`legacyVisitPhotos`, `legacyHivePhoto`, `notifyLegacy`) and the `legacy_key` matching of the
  inspection photo RPCs in `inspection.go`;
- the 90-day retention of inline removes (`legacyRemoveRetention`, `eraseOldLegacyRemoves` in
  `photo_sweep.go`).

What stays is the one-time conversion at start-up (`photo_boot.go`, step `inline_photos` in
`photo_migration`), so an install that skips 0.5 still converts its data; the `legacy_key` column
may stay as its marker.

## Deployment

One binary: `make build` builds the SPA, embeds it into `server/internal/web` and compiles
`server/bin/openbeehive`, which serves the app and the API (Connect-RPC over h2c, plus the photo
endpoints). The container image (`Dockerfile`) does the same in stages and runs as a non-root user
with `/data` for SQLite and blobs. Releases are tagged `vX.Y.Z`; `release.yml` publishes the signed,
attested image to `ghcr.io/openbeehive-org/openbeehive-app`.

## The decisions that shaped it

- [ADR 0002](adr/0002-local-first-sqlite-wasm.md): the device holds the data; sync merges deterministically.
- [ADR 0003](adr/0003-proto-contract-generated-stubs.md): the proto contract is the API; stubs are generated, not committed.
- [ADR 0004](adr/0004-single-binary-embedded-spa.md): one binary, one image, pluggable storage.
