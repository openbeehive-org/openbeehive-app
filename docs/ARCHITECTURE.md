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
- **Cursor and epoch.** Every database has a random epoch (`sync_state`). For protocol 1 a cursor is
  `<epoch>:<sequence number>:<fingerprint of the log entry at that number>`. `Pull` answers a cursor
  that is not part of this database's history with `RESET_REQUIRED` instead of changes: another
  epoch, a number beyond the end of the log, or a number whose entry is no longer the one the device
  saw. The last two are what a restored backup looks like, which hands the numbers after the backup
  out again.
- **Snapshot.** `Snapshot` returns the current rows of the workspace (tombstones included, personal
  rows only to their owner) with their field clocks, in pages cut by size, plus the cursor to
  continue from. The device merges each row column by column, so newer local edits survive and the
  outbox is untouched. A device uses it when it has no cursor, after `RESET_REQUIRED`, and after
  `SNAPSHOT_REQUIRED`.
- **Reset.** After `RESET_REQUIRED` the device also queues every local row for upload again, as
  ordinary changes grouped by the stamps the columns already carry. Where the server still has a
  change the merge does nothing; where the restore lost one, it comes back. 0.4.x apps get neither
  signal; one whose position lies beyond the restored log reads the log again from the start.
- **Compaction.** Once a day the server deletes log entries it received more than
  `BEEHIVE_CHANGELOG_RETENTION_DAYS` ago (default 180, 0 keeps everything) and stores the sequence
  number up to which it did (the horizon). A cursor older than the horizon gets
  `SNAPSHOT_REQUIRED`; a 0.4.x app gets the `UpgradeRequired` failure. A device that keeps syncing
  moves its cursor along with the log even when its workspace is quiet. Nothing else reads old log
  entries: who owns a personal task, for example, is stored on the task (`task.owner_id`). The job
  runs in the server process and assumes it is the only one: run exactly one instance per database.

## Deployment

One binary: `make build` builds the SPA, embeds it into `server/internal/web` and compiles
`server/bin/openbeehive`, which serves the app, the API (Connect-RPC over h2c) and, in self-host mode,
the blob files. The container image (`Dockerfile`) does the same in stages and runs as a non-root user
with `/data` for SQLite and blobs. Releases are tagged `vX.Y.Z`; `release.yml` publishes the signed,
attested image to `ghcr.io/openbeehive-org/openbeehive-app`.

## The decisions that shaped it

- [ADR 0002](adr/0002-local-first-sqlite-wasm.md): the device holds the data; sync merges deterministically.
- [ADR 0003](adr/0003-proto-contract-generated-stubs.md): the proto contract is the API; stubs are generated, not committed.
- [ADR 0004](adr/0004-single-binary-embedded-spa.md): one binary, one image, pluggable storage.
