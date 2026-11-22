# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
# Single-binary build: generate stubs -> build SPA -> embed -> compile server.
# Base images are pinned by digest; Renovate keeps tag and digest current.
#
# Only the final stage is built for the target platform. The others make
# output that is the same for every platform (generated code, the static SPA,
# a pure-Go binary cross-compiled for the target), so they run on the build
# machine's own architecture: no build step runs under emulation, which is
# slow and crashed the arm64 build with "Illegal instruction".

# --- Stage 1: generate proto stubs (Go + TS) ---
FROM --platform=$BUILDPLATFORM docker.io/bufbuild/buf:1.75.0@sha256:adf82c7170be6629ec81dc907921ac1699407cd11c4f7fd7521b13c74152f849 AS proto
WORKDIR /src
COPY buf.yaml buf.gen.yaml ./
COPY proto ./proto
RUN buf generate

# --- Stage 2: build the static SPA ---
FROM --platform=$BUILDPLATFORM docker.io/library/node:26-alpine@sha256:6bd9ab43c0ec0190ca7b2d193906cb8b7edf6e7f4afa43ddeccd6452710bffb5 AS app
WORKDIR /app
COPY app/package.json app/package-lock.json app/.npmrc ./
RUN npm ci
COPY app/ ./
COPY --from=proto /src/app/src/lib/proto ./src/lib/proto
# Shown in the app's footer: the commit (the context has no .git) and the
# build time, which is the commit's time when SOURCE_DATE_EPOCH is given.
ARG BUILD_COMMIT=""
ARG SOURCE_DATE_EPOCH=""
RUN BUILD_COMMIT="$BUILD_COMMIT" SOURCE_DATE_EPOCH="$SOURCE_DATE_EPOCH" npm run build

# --- Stage 3: compile the server with the SPA embedded ---
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS server
WORKDIR /src/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
COPY --from=proto /src/server/internal/gen ./internal/gen
COPY --from=app /app/build/. ./internal/web/dist/
# modernc.org/sqlite is pure Go -> CGO can stay disabled, and the binary is
# cross-compiled for the platform of the image being built.
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath -o /out/openbeehive ./cmd/server

# --- Final image ---
FROM docker.io/library/alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
RUN apk add --no-cache ca-certificates \
 && addgroup -g 65532 -S nonroot && adduser -u 65532 -S -G nonroot -h /data nonroot
COPY --from=server /out/openbeehive /usr/local/bin/openbeehive
# Self-host data (SQLite + blobs) lives under /data; mount a volume there.
USER 65532:65532
WORKDIR /data
EXPOSE 8080
ENTRYPOINT ["openbeehive"]
