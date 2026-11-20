# Contributing to Openbeehive

This is the **application** repository (Go backend + SvelteKit PWA). The
website and documentation live in
[`openbeehive-site`](https://github.com/openbeehive-org/openbeehive-site) and
[`openbeehive-docs`](https://github.com/openbeehive-org/openbeehive-docs).

## Ways to contribute

- Report bugs and request features via [issues](https://github.com/openbeehive-org/openbeehive-app/issues).
- Improve code, tests, or translations.
- Help triage and review pull requests.

## Development setup

Prerequisites:

- Go (version in `server/go.mod`) and Node (version in `app/.nvmrc`)
- `make tools`: the pinned gate tools (golangci-lint, govulncheck, go-licenses, fleetlint, buf) into
  `$(go env GOPATH)/bin`; [gitleaks](https://github.com/gitleaks/gitleaks) comes from its own release
- Git hooks: `prek install` (runs `.pre-commit-config.yaml`; `make check` on push)

```bash
make proto               # generate Go + TypeScript stubs from the proto contract
cp .env.example .env     # self-host profile: SQLite + filesystem
make run-server          # backend on :8080
make dev-app             # Vite dev server on :5173, in another shell
```

For the full single-binary build: `make build` then `./server/bin/openbeehive`.
The cloud profile (Postgres + MinIO) runs with `docker compose up -d`.

`make check` is the definition of green and exactly what CI runs: proto lint and breaking-change check,
lint (golangci-lint, go vet, prettier, eslint, svelte-check), tests, coverage gates, audit (govulncheck,
licenses, `go mod tidy`, npm audit, gitleaks), build and fleetlint. `make check-fast` is the quick
pre-commit subset. `make help` lists every target.

### Database backends

The server runs on SQLite, PostgreSQL and the MySQL family; the tested MySQL-family target is MariaDB
(the release pinned in `.github/workflows/check.yml`). `make check` tests on SQLite. CI runs the Go suite
a second time on Postgres and on MariaDB, and so can you:

```bash
podman run -d --rm --name beehive-pg -p 5433:5432 --tmpfs /var/lib/postgresql \
  -e POSTGRES_USER=beehive -e POSTGRES_PASSWORD=beehive -e POSTGRES_DB=beehive postgres:18-alpine
podman run -d --rm --name beehive-mariadb -p 3307:3306 --tmpfs /var/lib/mysql \
  -e MARIADB_ROOT_PASSWORD=beehive -e MARIADB_DATABASE=beehive mariadb:13.0.2

export PG_DSN='postgres://beehive:beehive@127.0.0.1:5433/beehive?sslmode=disable'
export MARIADB_DSN='root:beehive@tcp(127.0.0.1:3307)/beehive'
make test-db          # SQLite, then every backend whose DSN is set
make test-postgres    # one backend only
make test-mariadb
```

The targets set `BEEHIVE_TEST_DATABASE_DRIVER` (`sqlite`, `postgres` or `mysql`) and
`BEEHIVE_TEST_DATABASE_DSN`; every Go test that opens a database reads this one pair through
`server/internal/storage/sql/sqltest`, so `go test` on a single package works the same way. Each test
gets a database of its own and drops it afterwards: a schema inside the given database on Postgres, a
database named `<yours>_t…` on MariaDB. The MariaDB test user therefore needs the right to create and
drop databases (the root user of a throwaway container has it); the database named in the DSN itself
stays untouched.

Keep the containers' data in memory (`--tmpfs`, as above). Each test runs the whole migration chain;
with a tmpfs data directory the complete MariaDB run takes seconds, on a disk that syncs every DDL
statement it takes ten minutes and more per package. The targets therefore allow 30 minutes per
package (`DB_TEST_TIMEOUT`); pass `-timeout` yourself when you call `go test` directly against a
disk-backed server.

Anything that touches SQL, a migration or the sync write path should be green on all three before
you open a pull request. Rules for portable SQL:

- Migrations are plain `.sql` files in `server/internal/storage/sql/migrations/`, written once for
  all dialects: `TEXT`, `INTEGER`, `REAL`, `BOOLEAN` (literals `TRUE`/`FALSE`), `TIMESTAMP`, and
  `VARCHAR(n)` for every column that is indexed or unique. `Store.translate` adapts the few tokens
  that differ. A statement only one dialect needs goes into a file named `NNNN_name.<driver>.sql`
  (for example `0018_column_types.mysql.sql`); the other dialects record it as applied without running it.
- Text compares byte for byte on every backend (case, accents and trailing spaces matter); MariaDB
  tables are created with a binary collation for that. Compare case-insensitively on purpose, with
  `LOWER()`, where that is wanted.
- Timestamps of synced rows are strings on SQLite and points in time elsewhere. Pass them through
  the helpers in `server/internal/service` (`coerceFields`, `tsArg`, `tsCmp`) instead of comparing
  strings in SQL.

### Browser tests

`make e2e` builds the single binary and runs the Playwright suite in `app/e2e/` against it, in
Chromium at phone size: opening offline, two devices converging, recording and correcting records, the
second tab, the update prompt, a new device starting from a snapshot, sign-in, accounts and passkeys,
names that look like markup, a policy and accessibility check of every main route, and the demo. CI
runs it as a job of its own; it is not part of `make check`.

```bash
cd app && npx playwright install chromium     # once; on Linux add --with-deps for the system libraries
make e2e                                      # the whole suite, about a minute
make e2e E2E_ARGS='offline'                   # only the files whose name matches
make e2e E2E_ARGS='--headed --workers=1 -g "second tab"'
cd app && npx playwright show-report          # report of the last run, with a trace of every failed test
```

Every worker starts the binary on a fresh SQLite database and data directory (password login with an
instance admin, no mail server, maps off) on ports from 18400 up (`E2E_PORT_BASE` moves them). Tests
create their accounts, workspaces and records through the API (`e2e/support/api.ts`) and open one
browser context per device (`e2e/support/fixtures.ts`); a test that needs other server settings
starts an instance of its own with `startServer`. When you add a test:

- Wait for what a user would see; never sleep. Find elements by role, label or text.
- Use a new account per test (`api.signUp()`): tests of one worker share a server.
- A test for a bug that is not fixed yet is marked `test.fail()` with a comment that names the bug.
  It turns red once the bug is fixed; remove the marker then.

See [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) and the ADRs in `docs/adr/` for how the pieces fit
together, and the [documentation](https://docs.openbeehive.org) for the user-facing explanation.

## Project conventions

These keep the offline-first design coherent — please follow them:

- **All code (identifiers and comments) is English.** The only exception is the
  German *values* in `app/src/lib/i18n/locales/de.json` (and the other locale
  files); keys are always English.
- **Writes go through the local store**, never the CRUD RPCs. Use
  `lib/local/repo.ts` and `lib/local/history.ts`.
- **Keep merge logic in sync.** Conflict resolution is mirrored in
  `server/internal/sync/merge.go` and `app/src/lib/local/merge.ts` — changes to
  one must be reflected in the other.
- **The `.proto` contract is the source of truth** for the API. Change it first,
  then regenerate with `make proto`.
- **SQL must stay portable** across PostgreSQL, MariaDB/MySQL and SQLite (`?`
  placeholders + `db.Rebind`, portable column types); see "Database backends" above.
- **Translations:** user-facing strings must be added to every locale file
  (`en`, `de`, `fr`, `es`, `it`).

## Pull requests

1. Fork and create a topic branch from `main`.
2. Keep changes focused; write a clear description (the PR template guides you).
3. Make sure `make check` is green; add a `CHANGELOG.md` entry under `[Unreleased]`.
4. Reference any related issue (`Closes #123`).

## Commit messages

We use [Conventional Commits](https://www.conventionalcommits.org/) (e.g.
`feat: add queen breeding values`, `fix: correct varroa count rounding`); the commit-msg hook enforces it.

## License

By contributing, you agree that your contributions are licensed under the
project's [AGPL-3.0](LICENSE) license.
