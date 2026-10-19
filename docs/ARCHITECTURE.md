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
  internal/service    Connect-RPC handlers: sync (push/pull/subscribe), CRUD per entity, stats
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
  `change_log`; `Pull` serves the log from a cursor, limited to the scopes the user can see.
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
2. The sync engine pushes outbox changes to `SyncService.Push`. The server checks the scope, merges the
   payload field by field against the stored row's field clocks, and appends the change to the log.
3. Other devices call `Pull` with their cursor and receive the new log entries for their scopes, apply
   them with the same merge rules, and advance the cursor. `Subscribe` nudges connected devices when
   the cursor moves.
4. Any two devices that have seen the same changes converge to the same state, regardless of arrival
   order (the merge is deterministic; `server/internal/sync` and `app/src/lib/local/merge.ts` mirror
   each other and must change together).

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
