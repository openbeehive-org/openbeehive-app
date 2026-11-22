# Changelog

All notable changes to the Openbeehive application are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Footer** on every app page and on the sign-in pages: the app's version
  (linked to its release notes), the build's commit (linked to the source) and
  build time, and links to the documentation, the source code and
  openbeehive.org. Release images take the commit from the workflow and the
  build time from the tagged commit, so they stay reproducible. A fork can
  point the links elsewhere at build time with `BEEHIVE_DOCS_URL`,
  `BEEHIVE_SOURCE_URL` and `BEEHIVE_MARKETING_URL`.

### Fixed

- The apiary form showed the latitude and longitude fields twice when the map
  was switched off or the device was offline.
- "Use my location" is left out where the page may not ask for the position
  (a `Permissions-Policy: geolocation=()` header from the server or a proxy),
  instead of always failing. Chromium-based browsers report the policy; in
  others the button stays and says when the position is unavailable. A proxy
  that sets this header should allow `geolocation=(self)` to keep the button.

### Changed

- Updated the Alpine, Go and Node base images (system security fixes in the
  image), the Go modules (Postgres and MySQL drivers, SQLite, OAuth2) and the
  npm packages (Svelte, SvelteKit, Vite).

## [0.5.0] - 2026-11-21

Records can be corrected and deleted, photos move out of the
database into blob storage, accounts get password reset, deletion and member
management, and sync becomes robust against bad changes, wrong device clocks
and restored backups. Installed 0.4.x apps keep working with this server.

### Upgrading

Back up the database **and** the blob directory or bucket first. The server
refuses to start, and names the setting, where a required value is missing.
Details are in the
[upgrade notes](https://docs.openbeehive.org/self-hosting/upgrading#upgrading-to-050).

- **Session secret.** With any login method, `BEEHIVE_SESSION_SECRET` must be
  set and at least 32 bytes long. A new secret signs everyone out.
- **`.env`** in the working directory is now read by the binary; variables
  already set in the environment win.
- **CORS is same-origin by default.** If the app is served from another
  origin, list it in `BEEHIVE_CORS_ALLOWED_ORIGINS`; `*` no longer allows
  credentials.
- **Without login** the server answers only to `localhost`, IP addresses and
  the host of `BEEHIVE_PUBLIC_BASE_URL`; list other names in
  `BEEHIVE_ALLOWED_HOSTS`.
- **Behind a reverse proxy** set `BEEHIVE_TRUST_PROXY_HEADERS=true`, or
  sign-in throttling counts every user as the proxy's address.
- **Email verification** (`BEEHIVE_EMAIL_VERIFICATION=true`) requires
  `BEEHIVE_SMTP_HOST`. Links are no longer written to the log.
- **OIDC.** Every listed provider needs an issuer and a client id. A provider
  login is attached to an existing account only when both sides have a
  verified email address.
- **Compose files.** `docker-compose.yml` is the development stack and needs
  `BEEHIVE_SESSION_SECRET` and `MINIO_ROOT_PASSWORD`. Run Openbeehive with the
  new `docker-compose.selfhost.yml` or `docker-compose.prod.yml`.
  `docker-compose.demo.yml` publishes on `127.0.0.1` unless `BEEHIVE_BIND`
  is set.
- **Photos move to blob storage.** The first start converts photos stored
  inline in the database before it listens, so health checks fail until it
  is done; with many photos this takes a few minutes (the Compose files
  allow ten). `/files/` is gone and `BEEHIVE_BLOB_PUBLIC_URL` is
  ignored. A backup is now the database plus the blobs, and a restore means:
  stop, restore both together, start.
- **Blob directory.** With SQLite, `BEEHIVE_BLOB_DIR` must not contain the
  database file, or the server does not start; give the blobs a directory of
  their own.
- **Database migrations** take longer on a large database. SQLite rebuilds
  the visits table; PostgreSQL and MariaDB change tables in place, and
  MariaDB switches to a binary collation. A migration that stops part-way
  continues at the next start.
- **Change log.** Entries older than `BEEHIVE_CHANGELOG_RETENTION_DAYS`
  (default 180) are removed daily, for the first time 180 days after the
  upgrade at the earliest.
- **Turning login on** for an instance that ran without login moves its
  records into the instance admin's workspace at the next start. Sync every
  device first.
- **Accounts whose email addresses differ only in case** keep working, but
  email sign-in reaches only one of them; the log names each pair at start.
- **Invites** expire after 7 days and work only for the invited email
  address; issue older open invites again.
- **Limits.** One RPC message may be at most 16 MiB and a request may take
  at most 2 minutes (`BEEHIVE_HTTP_READ_TIMEOUT`,
  `BEEHIVE_HTTP_WRITE_TIMEOUT`); keep the proxy's body limit at 16 MiB or
  above.
- **Device clocks.** Changes stamped more than 10 minutes ahead of the
  server's time are refused. Updated apps correct their clock and send them
  again. A 0.4.x app with a clock that runs ahead keeps its changes but
  stops uploading until its clock is fixed or the app updates itself, so
  update the app on every device.
- **Scripts** that call `SyncService` must send `Openbeehive-Sync-Protocol: 1`.
  Update calls change only the fields sent, and `DeleteApiary` fails while
  the apiary has hives. The endpoints under `/auth/` and `/tenants/` accept
  only their documented method (otherwise 405) and need
  `Content-Type: application/json` when they take a body (otherwise 415);
  `/tenants/delete` needs `org_id`. `GetDashboard` counts a hive as due
  after 10 days instead of 21, and inspection dates more than a day ahead
  are refused.
- **Run exactly one server instance** per database. `BEEHIVE_NODE_ID` now
  defaults to a random id; remove it unless you set it on purpose.

### Security

- Sessions can be revoked: changing or resetting a password and "Log out
  everywhere" end the other sessions, and every request checks that the
  account is still a member of its workspace.
- Sign-in, password and reset attempts are throttled (10 failures per
  account and 30 per address in 15 minutes).
- The server sends a Content-Security-Policy and security headers, refuses
  cross-site requests to its plain-HTTP endpoints, and checks the host name
  when there is no login.
- Invites expire after 7 days and work only for the invited email address.
- Full-size photos are served only to members of their workspace, with a
  session or API key; the app removes location and camera data from images.
- Personal tasks reach only their owner, the author of a change is always
  set by the server, and database errors no longer reach API responses.
- Hive and apiary names are escaped on QR labels and map popups.
- Email addresses are unique regardless of upper and lower case.
- Each account keeps its own persistent local data per workspace on a shared
  device, so the next person to sign in neither sees nor uploads the
  previous one's unsent changes and personal tasks; an old store holding
  personal tasks is adopted only by the account that left them.
- With `BEEHIVE_DEMO=false` a demo account left from an earlier run is locked
  (no password, sessions, API keys or passkeys) instead of becoming a normal
  account with a public password.
- A changed account role applies without waiting for the session to
  expire, and a live `Subscribe` stream ends when its member is removed or
  the session is revoked.
- Rows written in a member's personal scope stay personal, ids starting with
  `user:` are refused, `ListEvents` shows personal events only to their
  owner, and an event's author is the user who created it.
- Inline photos that would take too much memory to decode get no thumbnail
  and are decoded outside the write lock, and the server clock no longer
  follows stamps beyond the 10-minute limit.
- CSV exports neutralise text that a spreadsheet would run as a formula,
  ZIP imports stop at 512 MiB unpacked, and reset and invite tokens are
  taken out of the address bar once read.

### Fixed

- One bad change no longer blocks or drops the rest of a sync: each change is
  applied on its own, and refused changes are listed under "Sync problems".
- Changes stamped by a device clock that runs ahead are refused, and updated
  apps correct their clock and send them again.
- Pull is scoped by workspace and the sync position only advances through
  Pull, so no device skips changes of others.
- Local writes are atomic, and only one tab works with the local database at
  a time.
- The installed app opens offline from a cold start, and updates no longer
  take over silently.
- Deleting a record also removes its timeline and statistics events;
  apiaries that still have hives cannot be deleted.
- The dashboard's due list ignores sensor readings and lost or dissolved
  colonies.
- Dates saved by earlier versions show on the day they were entered;
  charts show levels, sub-zero temperatures and time correctly.
- `ListHives` reports the real total; timestamps and decimals are stored
  exactly on SQLite, Postgres and MariaDB.
- A change refused because the account is read-only (the demo) is undone on
  screen with a message instead of lingering on the device.
- A received change that the app cannot apply no longer stops every later
  pull; it is listed under "Sync problems" as "could not be applied".
- The sign-in page says why an invite cannot be used (other address,
  expired, revoked) instead of continuing into an empty workspace.
- Default units follow the app's language before any are chosen.
- The printed report works under the Content-Security-Policy, is translated
  and uses the chosen units.
- Same-day visits are listed in the order they were recorded; CSV re-import
  and honey per year use the local calendar day.
- Import, ZIP and save errors are shown in the UI language; a CSV saved in
  Windows-1252 is read.
- Counts use plural forms in every language, the bottom bar uses short
  labels that fit at 360 px, and visit cards show outside climate.
- The hive list keeps its search, filters and sort in the URL; the Hives tab
  opens the unfiltered list.
- Photos of a refused or failed 0.4.x push leave no blobs behind; a retried
  push replaces the blobs of its earlier attempt.
- The dashboard highlights a due hive from the same 10 days that make it
  due, and `GetDashboard` counts those days in calendar days like the app,
  in the caller's `time_zone` (UTC when not given).
- Save and Add are switched off on every record form in the demo, and so are
  deleting, photo changes and ticking off tasks; the demo offers no import.
- Photos converted from 0.4.x apps get their image back after a database
  restore.
- An old unclaimed local store with nothing unsent is removed from the
  device after the first sync of the new one.
- The first sync of a large workspace is much faster.
- Pre-release databases that ran migrations 0015 and 0017 under their
  earlier names start again.

### Added

- **Correcting records.** Visits, harvests, treatments and queens can be
  edited and deleted in the app, with undo; edited harvests and treatments
  show "Amended on". The API gains `UpdateInspection`, `UpdateTreatment`,
  `UpdateHarvest` and `UpdateTask`.
- **Treatment register** to print per hive or for a whole apiary.
- **Photos** as synced records with a thumbnail; the full image (up to
  2048 px, 5 MiB) uploads in the background, opens in a viewer and is kept
  per device. WebP is supported.
- **Accounts.** Change password, "Forgot password?" by email or with a link
  from the instance admin, log out everywhere, delete the account, and a
  member list with roles, remove and leave.
- **Units and language** (kg/lb, °C/°F) saved with the account and applied on
  every device; records, exports and the API stay metric.
- **Dark theme** with a System / Light / Dark switch.
- **Sync status** chip, a "Sync problems" list with retry and discard,
  snapshots for new devices, and automatic re-upload after the server was
  restored from a backup. A protocol version lets the server ask outdated
  apps to update (`BEEHIVE_SYNC_MIN_PROTOCOL`).
- **Export and import.** Full backup as JSON or as a ZIP with photos, a
  server copy at `GET /export`, and an import with a preview that only adds
  by default, can overwrite or restore on request, and can be undone.
- **Sensor readings** (`inspection.source`) are charted but do not count as
  visits.
- **Maps** are configurable or can be switched off (`BEEHIVE_MAPS`,
  `BEEHIVE_MAP_TILE_URL`, `BEEHIVE_GEOCODER_URL`).
- First-run checklist, a working "New" menu, hive list search, filters and
  natural sorting, "Start from last visit", in-app QR scanning on every
  browser, and a "Try the demo" button on the sign-in page.
- MariaDB and Postgres are tested next to SQLite (`make test-db`), and a
  browser test suite runs with `make e2e`.
- The sync chip opens a status sheet with the last sync, changes and photos
  waiting to upload, refused changes and "Sync now".
- The workspace-wide treatment register (whole workspace, one apiary or one
  year) under Settings, and a Reason column in every register.
- The sign-in page names the workspace and masked address of an invite
  (`GET /auth/invite`).
- A skip link to the content, and opened forms take the focus.
- The start-up log names stored field stamps from the future and points to
  their repair, warns once about `X-Forwarded-For` without
  `BEEHIVE_TRUST_PROXY_HEADERS`, and states the CORS mode.

### Changed

- The visit form starts counts empty (empty means not recorded, 0 means
  observed none), has steppers, collapsed Climate and Activities sections,
  and no longer records honey; use a harvest instead. Old values stay
  visible.
- Edit forms save only the fields that were changed.
- The user interface says "workspace" instead of "tenant".
- RPC messages are capped at 16 MiB and requests at 2 minutes; unset
  inspection numbers are omitted instead of returned as 0.
- `/auth/verify`, `/auth/login` and `/auth/callback` send the browser to the
  sign-in page with an error instead of answering with JSON.
- The change log is compacted daily; a device that was away longer than the
  retention loads the current data instead.
- Settings is split into Preferences, Workspace, Data & sync and Account.
- A hive is due for inspection 10 days after its last visit, in the app and
  in `GetDashboard`.
- The demo says up front that it is read-only and its forms cannot be saved.
- Inviting an existing member answers 409 `already_member`; inviting an
  address again replaces its open invite.
- `update_mask` paths may be lowerCamelCase; an RPC request with a missing
  or unsupported `Content-Type` gets a JSON error. Without login,
  `/auth/me` answers `{"no_login": true}`.
- Blobs that no photo knows are kept at least 30 days (or the change-log
  retention) and never removed at start; only photo keys are swept.

### Removed

- Per-apiary sharing and the unused `apiary_share` table; the workspace is
  the unit of sharing.
- The `/files/` endpoint, `BEEHIVE_BLOB_PUBLIC_URL` and the unused sync
  page-size settings.

## [0.4.1] - 2026-11-20

Security and dependency release. No schema migration, no configuration
change and no change to the API on the wire.

### Security

- Go 1.27.4 fixes GO-2026-6859, an HTTP/1 server crash caused by a header race
  in `net/http`'s `MaxBytesReader`; the server binary and the image are built
  with it.
- `golang.org/x/net` 0.62.0 fixes two flaws of its HTML parser and renderer
  (GO-2026-6860, GO-2026-6866).
- `golang.org/x/crypto` 0.59.0 (GO-2026-5932, the unmaintained `openpgp`
  package). The server uses `bcrypt` from this module only.

### Changed

- The server speaks Connect through connect-go v2. Handlers, clients and the
  auth interceptors are ported, all services share one `connect.Server`, and
  the read limit stays unbounded so large sync pushes keep working.
- SQLite driver `modernc.org/sqlite` 1.61.0.
- The image is based on Alpine 3.24 and built with Node 26.13; the web app
  ships the current patch releases of SvelteKit, Svelte and Vite.
- Renovate completes `go.sum` without the generated Connect stubs and holds
  TypeScript at 6.x until SvelteKit, svelte-check and typescript-eslint
  accept 7.

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

[Unreleased]: https://github.com/openbeehive-org/openbeehive-app/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/openbeehive-org/openbeehive-app/releases/tag/v0.5.0
[0.4.1]: https://github.com/openbeehive-org/openbeehive-app/releases/tag/v0.4.1
[0.3.1]: https://github.com/openbeehive-org/openbeehive-app/releases/tag/v0.3.1
[0.3.0]: https://github.com/openbeehive-org/openbeehive-app/releases/tag/v0.3.0
[0.2.3]: https://github.com/openbeehive-org/openbeehive-app/releases/tag/v0.2.3
[0.2.2]: https://github.com/openbeehive-org/openbeehive-app/releases/tag/v0.2.2
[0.2.1]: https://github.com/openbeehive-org/openbeehive-app/releases/tag/v0.2.1
[0.1.0]: https://github.com/openbeehive-org/openbeehive-app/releases/tag/v0.1.0
