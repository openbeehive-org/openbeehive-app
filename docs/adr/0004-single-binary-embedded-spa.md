# 4. One binary with the SPA embedded, pluggable storage

Status: accepted

## Context

Self-hosting beekeepers want one thing to run. Associations and the hosted service need Postgres and
object storage behind the same code.

## Decision

The SvelteKit app builds to a static SPA that is embedded into the Go binary (`server/internal/web`),
which serves the app, the API and, in self-host mode, blob files on one port. Storage sits behind two
interfaces with a deployment profile choosing the defaults: `selfhost` = SQLite + filesystem,
`cloud` = Postgres + MinIO/S3. The container image is the same binary on Alpine, running as a non-root
user with `/data` for state.

## Consequences

SQL stays portable (`?` placeholders, `Rebind`, portable types). The app is built before the server in
every build path (`make build`, Dockerfile). Serving the SPA from the API origin means no CORS for the
embedded app; separate app domains are supported through `BEEHIVE_CORS_*`.
