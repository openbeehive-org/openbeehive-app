# Changelog

All notable changes to the Openbeehive application are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.4.0] - 2026-10-19

The repository, the image and the Go module live under the `openbeehive-org`
organisation, the container runs as an unprivileged user and every release is
signed. No schema migration. One manual step for self-hosters: a Docker volume
created by 0.3.x or earlier must be chowned to uid 65532 once (see
"Upgrading" in the docs).

### Added

- **Signed releases.** The image carries BuildKit SLSA provenance and SBOM
  attestations, is signed keyless with cosign (Fulcio certificate bound to the
  release workflow, logged in Rekor) and gets a GitHub build-provenance
  attestation; the GitHub release ships the SPDX SBOM. Verification commands
  are in the release workflow header and in the docs.
- **Engineering baseline.** `make check` is the single definition of green
  (proto lint and breaking-change check, golangci-lint, prettier, eslint,
  svelte-check, tests with coverage gates, govulncheck, licence allowlists,
  gitleaks, build, fleetlint); prek hooks run the fast subset on commit;
  CodeQL and Renovate run on the repository. ADRs and an architecture
  overview are in `docs/`.

### Changed

- **Container runs as uid 65532** with `/data` as its working directory and a
  read-only-root-friendly layout; base images are pinned by digest. The
  defaults for the SQLite file and the blob directory therefore resolve to
  `/data`.
- **New home.** Image: `ghcr.io/openbeehive-org/openbeehive-app`
  (`ghcr.io/johnnycube/openbeehive-app` keeps the tags up to 0.3.1). Go
  module: `github.com/openbeehive-org/openbeehive-app/server`. Links and
  Docusaurus references follow.
- **Toolchain.** Go 1.27.2; SvelteKit 3 with adapter-static 4 (config lives
  in `vite.config.ts`, `#lib` imports, service worker in its own TypeScript
  project and precaching the prerendered shell as well as the immutable
  assets); Vite 8.3, Vitest 5, Svelte 5.57; buf 1.74 with pinned remote
  plugins (protocolbuffers/go 1.36.12, connectrpc/go 1.21.1, bufbuild/es
  2.16). All Go modules and npm packages at their latest releases; TypeScript
  stays on 6.x until svelte-check and typescript-eslint accept 7.
- **UI icons.** The app uses Lucide icons instead of emoji for pins, edit,
  delete, camera, print, keys and the inspection chips, which now read as
  text ("Honey 3 kg") in lists and on the printable hive card.
- **Messages.** Server and app messages use plain ASCII punctuation; the
  demo read-only and invite-only errors were reworded accordingly.
- **Types.** The frontend no longer uses `any` for Leaflet, SQLite, the sync
  payloads and the WebAuthn options; the data layer's row types are used
  throughout.

### Security

- Go 1.27.2 and `golang.org/x/net` 0.60 clear every open stdlib and x/net
  advisory govulncheck reported against 1.26; `npm audit` is clean on the
  updated frontend and the `cookie` override is gone with SvelteKit 3.

## [0.3.1] - 2026-09-29

Dependency maintenance. No functional changes and no migration.

### Security

- `devalue` 5.9.4 clears GHSA-9rgm-9g3h-6x36 (denial of service via malformed
  input).
- `cookie` 0.7.x and the `esbuild` that `svelte-i18n` pulls for its CLI
  (0.25.x) are lifted through npm overrides; `postcss`, `vitest` and
  `@sveltejs/kit` are updated within their ranges.

The app is prerendered and served as a static bundle, so none of these
packages run on the server.

## [0.3.0] - 2026-09-15

The integration API release: every entity is reachable through a served
Connect-RPC service, scripts and devices authenticate with API keys, and
the proto contract is English throughout. Breaking for external clients
that generated code from the old protos (see "Proto renames").

### Added

- **Integration API.** Every entity now has a served Connect-RPC service:
  `QueenService`, `InspectionService`, `TaskService`, `TreatmentService`,
  `EventService` and `StatsService` join `ApiaryService`, `HiveService` and
  `SyncService`. Writes go through the same per-field merge and change log as
  the app's sync, so a record created through the API (for example a hive
  scale posting `CreateInspection` with `weight_kg`) shows up on every device
  on its next sync and merges with later edits field by field. Server-side
  writes mirror the app's flows: creating a hive opens a placement interval
  and records a CREATED event, `RelocateHive` closes and opens placements,
  `CreateQueen` ends the previous reign, inspections and treatments freeze
  the apiary and reigning queen at their date.
- **`HarvestService`** (`CreateHarvest`, `ListHarvests`, `DeleteHarvest`):
  records the harvest with frozen apiary and queen plus the HARVEST fact
  event, so API-recorded harvests feed the honey statistics like the app's.
- **API keys** for scripts and devices: Settings → API keys mints long-lived
  keys (`obhk_…`, shown once, stored hashed) bound to your account and the
  active tenant; send them as `Authorization: Bearer`. Keys are managed with
  a normal session only, never for the demo account, and die with the
  tenant membership. Keys can be read-only (`Get*`/`List*`/`Pull`/`Subscribe`
  only) and can carry an expiry (30/90/365 days or never). Tenant admins
  see and can revoke every member's key of their tenant (`?tenant=1` on
  the list endpoint; Settings shows "Keys of other members"). New table
  `api_key` (migration 0014).
- Offset pagination: `page_token` is honoured and `next_page_token` returned.

### Changed

- **Proto renames** (English contract): `SetTaskErledigt` → `SetTaskDone`,
  `nur_open` → `only_open`, `nur_activee` → `only_active`,
  `ersets_vorherige` removed (replacement is always applied),
  `eventse` → `events`, `pro_*` → `per_*`, `Dashboard`/`HoneyStats` →
  `GetDashboard`/`GetHoneyStats`. `PhotoUploadUrl` is replaced by
  `AddInspectionPhoto`/`RemoveInspectionPhoto` (photos are inline data-URL
  thumbnails, as in the app). `Queen.trait_*` dropped (never stored);
  `Queen.replaced_at` added. `CreateInspectionRequest` gains the stock-card
  fields (`weight_kg`, `brood_frames`, ...).
- `ApiaryService`/`HiveService` writes now go through the change log too and
  delete softly (previously they bypassed sync and hard-deleted rows).
- **`app/.env.example`** now names the variables the app actually reads
  (`BEEHIVE_API_URL`, `BEEHIVE_MARKETING_URL`, `BEEHIVE_PUBLIC_URL`).
- **Docs and comments corrected:** stale first-account-admin notes, role lists,
  version pins and the "apiary-level sharing" claim; unused locale strings
  removed from the app bundle.

## [0.2.3] - 2026-09-11

### Added

- **Skeletons and a syncing hint instead of premature empty states:** on
  refresh, pages showed "create your first apiary" (and the dashboard hard
  zeros) before the first local read, and kept showing it while a fresh device
  was still downloading its data. The dashboard and the apiary, hive and task
  lists now render shimmering skeletons while the local read is pending, a
  "Syncing your data…" hint while the first pull is still outstanding, and the
  real empty state only once the data is known to be authoritative.
- **Progressive first sync:** a new initial-sync state tracks whether this
  device holds a full replica yet, and pulls refresh open pages after every
  applied batch instead of once at the end, so a large first sync fills the
  screen as it arrives.

### Fixed

- **Reloads no longer start from an empty store:** installing the OPFS storage
  pool can race the previous worker that still holds its access handles; the
  install is now retried briefly before falling back to in-memory storage, and
  when the fallback does happen (private browsing, a second tab holding the
  pool) a toast says that changes will not persist on this device instead of a
  console warning nobody sees.
- **Faster start after a reload:** the auth session is resolved with parallel
  requests, so background sync — and the first data on a fresh device —
  begins one round-trip earlier.
- Action icons on the hive page are centered again.

## [0.2.2] - 2026-08-01

### Fixed

- **Saving could fail permanently with `SQLITE_CANTOPEN` ("unable to open
  database file")** — most visible on the demo as an add button that did
  nothing. Local OPFS storage pools created by earlier versions kept their
  small slot count forever; the app now reserves the minimum capacity on
  every start and grows the pool automatically if a write still runs out of
  slots.
- **Sync no longer stalls behind a failing push:** fresh data is pulled even
  while the outbox cannot be sent, and pushes rejected as read-only (the demo
  account, unwritable scopes) are dropped instead of retrying every 15
  seconds — the affected changes simply stay on the device.
- **Sync protocol, photo lists:** removes now carry the origin's observed
  add-tags, so a photo re-added concurrently on another device survives a
  remove (true add-wins). Deltas from older versions keep the previous
  behavior.
- **Failed local writes are visible:** the apiary form reports save errors
  inline and keeps the typed input; everywhere else an error toast appears
  instead of a silently dead button.

### Added

- **Interactive location picker for apiaries:** a map with a draggable pin
  that stays in sync with the address field (OpenStreetMap/Nominatim), with
  visible lookup states — searching, location set, no match, service
  unreachable.
- **Passkeys are bound to registered accounts:** enrollment lives in
  settings, sign-in offers the passkey only for accounts that registered one.
- **Test suites for the offline-first core.** Web (vitest on a real SQLite):
  per-field LWW and OR-Set merge semantics, HLC ordering, repo/outbox writes,
  sync error paths, geocoding states, and a two-device offline convergence
  simulation. Server (Go): merge mirrors of the client tests, read-only-guard
  coverage, and the OR-Set observed-tags round-trip through Push.

## [0.2.1] - 2026-07-31

### Added

- **Invite management** in settings: the invite link is now shown (with a copy
  button) right after creating it, all open invites are listed with their
  signup links, and invites can be revoked. New owner-only endpoints
  `GET /tenants/invites` and `POST /tenants/invite/revoke`.
- **Invite emails** — the invitee receives the signup link by email when SMTP
  is configured (shared mailer with the verification flow); without SMTP the
  link is logged as before and always shown in the UI.
- **Tenant deletion** (danger zone) — a tenant admin can permanently delete a
  tenant via `POST /tenants/delete`: all records, memberships, invites and the
  change log are removed in one transaction, inspection photo blobs are purged
  from the object store, and the session moves to a remaining tenant (or a
  fresh personal one). Confirmed in the UI, translated in all five languages.

### Security

- **FS blob store:** client-supplied blob keys (synced `photo_keys`) can no
  longer escape the storage base directory (path-traversal guard in the
  filesystem backend).

## [0.1.0] - 2026-07-17

First public release. 🐝

### Added

- **Offline-first PWA** (SvelteKit, Svelte 5) with a local SQLite-WASM store on
  OPFS — fully usable in the bee yard with or without a signal.
- **Domain model:** Apiaries, Hives, Queens, Inspections, Tasks, Events,
  Harvests and Treatments.
- **Per-hive records** (stock cards) with colony strength, brood, stores,
  varroa, behaviour and per-visit activities.
- **Queen management** with the international marking-colour scheme and full
  reign history.
- **Location history** — hives tracked across apiaries over time.
- **Honey harvests** with batch, water content and best-before, feeding season
  statistics.
- **QR codes** per hive — print labels and scan straight into a hive's record.
- **Sync engine** with HLC timestamps, per-field last-writer-wins and OR-Sets;
  apiary-level sharing via scopes.
- **Pluggable backends:** PostgreSQL · MySQL · SQLite, and MinIO/S3 ·
  filesystem, selected via `BEEHIVE_DEPLOYMENT_PROFILE` (`cloud` | `selfhost`).
- **Connect-RPC API** (gRPC + HTTP/JSON) with the `.proto` contract as the
  source of truth.
- **OIDC authentication** with multiple providers.
- **Email/password onboarding** with optional email verification, multi-tenant
  invites, and an **invite-only mode** (`BEEHIVE_REGISTRATION=false`) that
  disables open registration and shows a notice on the sign-in screen.
- **Single-binary self-hosting** (SQLite + filesystem, no Docker required) and a
  Docker image for the cloud profile.
- **Multi-language UI:** English, German, French, Spanish, Italian.

[0.3.1]: https://github.com/openbeehive-org/openbeehive-app/releases/tag/v0.3.1
[0.3.0]: https://github.com/openbeehive-org/openbeehive-app/releases/tag/v0.3.0
[0.2.3]: https://github.com/openbeehive-org/openbeehive-app/releases/tag/v0.2.3
[0.2.2]: https://github.com/openbeehive-org/openbeehive-app/releases/tag/v0.2.2
[0.2.1]: https://github.com/openbeehive-org/openbeehive-app/releases/tag/v0.2.1
[0.1.0]: https://github.com/openbeehive-org/openbeehive-app/releases/tag/v0.1.0
