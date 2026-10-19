# 2. Local-first data with deterministic sync

Status: accepted

## Context

Beekeeping happens at the apiary, often without signal. A request/response app is unusable there, and
"offline mode" bolted onto a server-centric app leaves conflicts for the user to resolve by hand.

## Decision

Every device keeps a full copy of the data it may see in SQLite-WASM on OPFS. The UI reads and writes
only that copy. A background engine exchanges changes with the server; merges are deterministic: hybrid
logical clocks order every change, scalars are last-writer-wins per field, list fields are add-wins
OR-Sets, and history events are append-only. The server applies the same rules and keeps a change log
that devices pull from a cursor, limited to the apiary scopes they can access.

## Consequences

The merge engine exists twice (`server/internal/sync`, `app/src/lib/local/merge.ts`) and must change
together. Schema changes must stay portable across Postgres, MySQL and SQLite. The app never shows a
conflict dialog; what it shows is whatever the merge rules decide.
