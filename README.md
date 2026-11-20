<div align="center">

# Openbeehive

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

## Quick start — self-hosted

One container, SQLite and your files in a named volume:

```bash
docker run -d --name openbeehive \
  -p 127.0.0.1:8080:8080 \
  -v openbeehive-data:/data \
  ghcr.io/openbeehive-org/openbeehive-app:latest
```

Open <http://localhost:8080>. Out of the box there is **no login**: this is
single-user mode, meant for your own machine, so the port is published on
`127.0.0.1` only and the server answers only to `localhost`, IP addresses and
the host of `BEEHIVE_PUBLIC_BASE_URL` (add names with `BEEHIVE_ALLOWED_HOSTS`).

To reach it from other devices or share it, turn login on.
[`docker-compose.selfhost.yml`](docker-compose.selfhost.yml) is the same
setup as a compose file; put the settings in an `.env` file next to it:

```bash
BEEHIVE_BIND=0.0.0.0
BEEHIVE_PUBLIC_BASE_URL=https://bees.example.org
BEEHIVE_PASSWORD_AUTH=true
BEEHIVE_ADMIN_EMAIL=you@example.org
BEEHIVE_ADMIN_PASSWORD=change-me-please
BEEHIVE_SESSION_SECRET=...    # openssl rand -base64 32
```

```bash
docker compose -f docker-compose.selfhost.yml up -d
```

Records made before login was switched on move into the admin's workspace at
the next start. Put a reverse proxy with TLS in front for anything beyond your
LAN, and set `BEEHIVE_TRUST_PROXY_HEADERS=true` behind it.

### Without Docker (single binary)

Requires Go (`server/go.mod`), Node (`app/.nvmrc`) and [**buf**](https://buf.build) (`make tools`).

```bash
cp .env.example .env          # BEEHIVE_DEPLOYMENT_PROFILE=selfhost; edit as needed
make proto                    # generate Go + TypeScript stubs from the .proto contract
make build                    # build the app, embed it, compile the server
./server/bin/openbeehive      # reads .env from the current directory; serves the app + API at :8080
```

## Quick start — several users (Postgres + MinIO)

[`docker-compose.prod.yml`](docker-compose.prod.yml) runs the released image
with Postgres and MinIO, login always on and nothing but the app port
published (on `127.0.0.1`, for your reverse proxy). It refuses to start until
every secret is set; the file lists them:

```bash
docker compose -f docker-compose.prod.yml up -d
```

Run exactly one server instance per database.
[`docker-compose.demo.yml`](docker-compose.demo.yml) is the same plus the
public read-only demo; [`docker-compose.yml`](docker-compose.yml) builds the
server from this checkout for development and is not a production template.

See the [self-hosting guide](https://docs.openbeehive.org) for full configuration.

## Development

```bash
make tools                    # pinned gate tools (golangci-lint, govulncheck, go-licenses, fleetlint, buf)
make proto                    # generate stubs
make run-server               # backend on :8080 (reads .env)
make dev-app                  # Vite dev server on :5173; set BEEHIVE_CORS_ALLOWED_ORIGINS=http://localhost:5173 in .env
make check                    # the definition of green: lint, tests, coverage, audit, build, fleetlint
```

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for conventions, [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)
for how the pieces fit together, and the [documentation](https://docs.openbeehive.org) for design deep-dives.

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
