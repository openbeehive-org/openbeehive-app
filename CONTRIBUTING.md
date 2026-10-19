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

You'll need **Go 1.25+**, **Node 24+**, and [**buf**](https://buf.build).

```bash
# 1) generate code from the proto contract (Go + TypeScript stubs)
make proto

# 2) run the backend (self-host profile: SQLite + filesystem)
cp .env.example .env
make run-server          # :8080

# 3) run the app in another shell
make dev-app             # Vite on :5173
```

For the full single-binary build: `make build` then `./server/bin/openbeehive`.
The cloud profile (Postgres + MinIO) runs with `docker compose up -d`.

See the [documentation](https://docs.openbeehive.org) for the architecture.

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
- **SQL must stay portable** across PostgreSQL, MySQL and SQLite (`?`
  placeholders + `db.Rebind`, portable column types).
- **Translations:** user-facing strings must be added to every locale file
  (`en`, `de`, `fr`, `es`, `it`).

## Pull requests

1. Fork and create a topic branch from `main`.
2. Keep changes focused; write a clear description (the PR template guides you).
3. Make sure it builds (`make build`).
4. Reference any related issue (`Closes #123`).

## Commit messages

We use [Conventional Commits](https://www.conventionalcommits.org/) (e.g.
`feat: add queen breeding values`, `fix: correct varroa count rounding`).

## License

By contributing, you agree that your contributions are licensed under the
project's [AGPL-3.0](LICENSE) license.
