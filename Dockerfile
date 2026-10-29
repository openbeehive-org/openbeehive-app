# syntax=docker/dockerfile:1
# Single-binary build: generate stubs -> build SPA -> embed -> compile server.
# Base images are pinned by digest; Renovate keeps tag and digest current.

# --- Stage 1: generate proto stubs (Go + TS) ---
FROM docker.io/bufbuild/buf:1.75.0@sha256:adf82c7170be6629ec81dc907921ac1699407cd11c4f7fd7521b13c74152f849 AS proto
WORKDIR /src
COPY buf.yaml buf.gen.yaml ./
COPY proto ./proto
RUN buf generate

# --- Stage 2: build the static SPA ---
FROM docker.io/library/node:26-alpine@sha256:6f08a523443ddedf52941874c203475b0ad503e859ad70cb26ab8be9a517dbd5 AS app
WORKDIR /app
COPY app/package.json app/package-lock.json app/.npmrc ./
RUN npm ci
COPY app/ ./
COPY --from=proto /src/app/src/lib/proto ./src/lib/proto
RUN npm run build

# --- Stage 3: compile the server with the SPA embedded ---
FROM docker.io/library/golang:1.27-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS server
WORKDIR /src/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
COPY --from=proto /src/server/internal/gen ./internal/gen
COPY --from=app /app/build/. ./internal/web/dist/
# modernc.org/sqlite is pure Go -> CGO can stay disabled.
RUN CGO_ENABLED=0 go build -trimpath -o /out/openbeehive ./cmd/server

# --- Final image ---
FROM docker.io/library/alpine:3.23@sha256:85fe1e81d6758c208f3e1eed4338a1997e19d4be002d4dd32d3100c9a8c010a0
RUN apk add --no-cache ca-certificates \
 && addgroup -g 65532 -S nonroot && adduser -u 65532 -S -G nonroot -h /data nonroot
COPY --from=server /out/openbeehive /usr/local/bin/openbeehive
# Self-host data (SQLite + blobs) lives under /data; mount a volume there.
USER 65532:65532
WORKDIR /data
EXPOSE 8080
ENTRYPOINT ["openbeehive"]
