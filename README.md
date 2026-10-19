<div align="center">

# 🐝 Openbeehive

**Offline-first beekeeping records — a free hosted service _and_ self-hostable software.**

For hobby and professional beekeepers alike.

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-c77f22.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/openbeehive-org/openbeehive-app?color=c77f22)](CHANGELOG.md)

[Website](https://openbeehive.org) · [App](https://app.openbeehive.org) · [Docs](https://docs.openbeehive.org)

</div>

---

Openbeehive keeps your whole bee year in one place: apiaries, hives, queens,
inspections, tasks and harvests — cleanly documented. The app is **offline-first**:
it stores everything in a local database on your device and syncs to the server
when you're online, so it stays fast and fully usable out in the bee yard, with
or without a signal.

> **Status:** early releases (see the [changelog](CHANGELOG.md)). Already useful; expect rough edges.

## Features

- **Complete hive records** — every inspection with queen, brood, stores, varroa,
  behaviour and photos, chronological per colony.
- **Queens & breeding** — international marking-colour scheme, origin and full
  reign history.
- **Location history** — follow each hive across apiaries and years.
- **Treatment log & harvests** — batches, withdrawal periods, honey lots with
  best-before; audit-ready.
- **QR codes** — print a label per hive and scan straight into its record.
- **Multi-device sync** — conflict-free (HLC + per-field LWW + OR-Sets);
  shared tenants with invites for teams and clubs.
- **Multi-language** — English, German, French, Spanish, Italian.
- **Your data, your choice** — use the free hosted service or self-host.

## Two deployment profiles

`BEEHIVE_DEPLOYMENT_PROFILE` sets sensible defaults; every option is individually
overridable.

| Profile     | Database   | Blob storage   | Target                                |
|-------------|------------|----------------|---------------------------------------|
| `cloud`     | PostgreSQL | MinIO / S3     | central public service, scaling       |
| `selfhost`  | SQLite     | local filesystem | one beekeeper, one server, one binary |

```
BEEHIVE_DATABASE_DRIVER=postgres|mysql|sqlite
BEEHIVE_DATABASE_DSN=...
BEEHIVE_BLOB_BACKEND=minio|fs
```

The **same binary** covers both: a Postgres + MinIO cloud service, or SQLite
plus a data folder on one host.

## Tech stack

- **Frontend:** SvelteKit (Svelte 5 runes), mobile-first PWA, local-first
  (SQLite-WASM on OPFS).
- **Backend:** Go, API via [Connect-RPC](https://connectrpc.com/) (gRPC + HTTP/JSON).
- **Auth:** email + password (with invites), passkeys (WebAuthn), OIDC (multiple providers).
- **Database (pluggable):** PostgreSQL · MySQL · SQLite.
- **Blob storage (pluggable):** MinIO/S3 · local filesystem.

## Quick start — self-hosted (single binary)

Requires **Go 1.25+**, **Node 24+** and [**buf**](https://buf.build).

```bash
cp .env.example .env          # BEEHIVE_DEPLOYMENT_PROFILE=selfhost
make proto                    # generate Go + TypeScript stubs from the .proto contract
make build                    # build the app, embed it, compile the server
./server/bin/openbeehive      # serves the app + API at :8080
```

## Quick start — Docker (cloud profile)

```bash
docker compose up -d          # Postgres + MinIO + the server (which serves the SPA)
```

A prebuilt image is published to the GitHub Container Registry on each release:

```bash
docker run -p 8080:8080 ghcr.io/openbeehive-org/openbeehive-app:latest
```

See the [self-hosting guide](https://docs.openbeehive.org) for full configuration.

## Development

```bash
make proto                    # generate stubs
make run-server               # backend on :8080
make dev-app                  # Vite dev server on :5173
```

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for conventions and architecture,
and the [documentation](https://docs.openbeehive.org) for design deep-dives.

## Project layout

```
proto/      Connect-RPC contract (.proto) — source of truth for the API
server/     Go backend (config, auth, storage, sync, service handlers, embedded SPA)
app/        SvelteKit PWA (local-first store, sync engine, routes, components)
```

## Related repositories

- **[openbeehive-site](https://github.com/openbeehive-org/openbeehive-site)** — marketing site (openbeehive.org)
- **[openbeehive-docs](https://github.com/openbeehive-org/openbeehive-docs)** — documentation (docs.openbeehive.org)

## License

Openbeehive is licensed under the **[GNU AGPL-3.0](LICENSE)**. If you run a
modified version as a network service, you must make your modified source
available to its users.
