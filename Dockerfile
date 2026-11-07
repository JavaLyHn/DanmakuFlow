# syntax=docker/dockerfile:1
FROM node:24.22.2-bookworm-slim AS web-build
WORKDIR /src/web
ENV NODE_OPTIONS=--max-old-space-size=512
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.25.13-bookworm AS go-build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOMAXPROCS=2 GOMEMLIMIT=768MiB
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
# Packages and the three binaries compile serially to limit build-time memory.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -p 1 -trimpath -ldflags="-s -w" -o /out/server ./cmd/server && \
    go build -p 1 -trimpath -ldflags="-s -w" -o /out/consumer ./cmd/consumer && \
    go build -p 1 -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

FROM scratch AS runtime
WORKDIR /app
COPY --from=go-build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=go-build /out/ /app/
COPY --from=web-build /src/web/dist/ /app/web/
COPY LICENSE /app/LICENSE
USER 65532:65532
EXPOSE 8080
CMD ["/app/server", "-port", "8080", "-web-dir", "/app/web"]
