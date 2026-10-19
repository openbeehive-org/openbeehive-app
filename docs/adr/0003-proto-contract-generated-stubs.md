# 3. The proto contract is the API; stubs are generated, not committed

Status: accepted

## Context

The app, scripts and devices all talk to the server. Hand-written client code and JSON shapes drifted
between the Go server and the TypeScript app.

## Decision

`proto/openbeehive/v1` is the single source of truth for every message and service. Go (protobuf +
Connect) and TypeScript (protobuf-es) stubs are generated with `buf generate` using remote plugins pinned
in `buf.gen.yaml`, into gitignored directories. CI, the Dockerfile and `make` regenerate them. Breaking
changes are caught by `buf breaking` against the previous release tag.

## Consequences

A contract change is a proto change first, then `make proto`, then code. Plugin versions must stay in
step with the runtime libraries in `server/go.mod` and `app/package.json` (connect v1, protobuf-es v2).
Nothing in `server/internal/gen` or `app/src/lib/proto` is ever edited by hand.
