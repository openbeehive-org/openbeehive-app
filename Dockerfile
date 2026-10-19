# syntax=docker/dockerfile:1
# Single-binary build: generate stubs -> build SPA -> embed -> compile server.

# --- Stage 1: generate proto stubs (Go + TS) ---
FROM bufbuild/buf:latest AS proto
WORKDIR /src
COPY buf.yaml buf.gen.yaml ./
COPY proto ./proto
RUN buf generate

# --- Stage 2: build the static SPA ---
FROM node:24-alpine AS app
WORKDIR /app
COPY app/package.json app/package-lock.json ./
RUN npm ci
COPY app/ ./
COPY --from=proto /src/app/src/lib/proto ./src/lib/proto
RUN npm run build

# --- Stage 3: compile the server with the SPA embedded ---
FROM golang:1.27-alpine AS server
WORKDIR /src/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
COPY --from=proto /src/server/internal/gen ./internal/gen
COPY --from=app /app/build/. ./internal/web/dist/
# modernc.org/sqlite is pure Go -> CGO can stay disabled.
RUN CGO_ENABLED=0 go build -o /out/openbeehive ./cmd/server

# --- Final image ---
FROM alpine:latest
RUN apk add --no-cache ca-certificates
COPY --from=server /out/openbeehive /usr/local/bin/openbeehive
EXPOSE 8080
ENTRYPOINT ["openbeehive"]
