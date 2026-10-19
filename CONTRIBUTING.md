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
- **SQL must stay portable** across PostgreSQL, MySQL and SQLite (`?`
  placeholders + `db.Rebind`, portable column types).
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
